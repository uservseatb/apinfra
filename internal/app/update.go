package app

import (
	"context"
	"log"
	"time"

	"github.com/uservseatb/apinfra/internal/acme"
	"github.com/uservseatb/apinfra/internal/certstore"
	"github.com/uservseatb/apinfra/internal/config"
	"github.com/uservseatb/apinfra/internal/domain/certmanagement"
	"github.com/uservseatb/apinfra/internal/domain/deployment"
	"github.com/uservseatb/apinfra/internal/domain/docker"
	"github.com/uservseatb/apinfra/internal/domain/nginx"
	"github.com/uservseatb/apinfra/internal/events"
	"github.com/uservseatb/apinfra/internal/infrastructure"
	"github.com/uservseatb/apinfra/internal/infrastructure/adapters"
	"github.com/uservseatb/apinfra/internal/sys"
)

// RunUpdate is the composition root: load host config, wire adapters into domain
// services, and run one GitOps update cycle.
// Operational failures are soft-failed so the systemd timer unit stays healthy.
func RunUpdate(ctx context.Context, version string) error {
	if ctx == nil {
		log.Printf("WARN nil context (soft-fail)")
		LogRunSeparator()

		return nil
	}

	cfg, err := config.LoadHostConfig()
	if err != nil {
		log.Printf("WARN config: %v (soft-fail, will retry)", err)
		LogRunSeparator()

		return nil
	}

	log.Printf(
		"starting gitops agent version=%s infraDir=%s namespace=%s repo=%s",
		version,
		cfg.InfraDirPath,
		cfg.Namespace,
		cfg.RepoURL,
	)

	started := time.Now()

	runWiredUpdate(ctx, cfg)

	log.Printf("done duration=%s", time.Since(started).Round(time.Millisecond))
	LogRunSeparator()

	return nil
}

// LogRunSeparator prints two dash lines so the next journald block is visually separated.
func LogRunSeparator() {
	log.Print("-")
	log.Print("-")
}

func runWiredUpdate(ctx context.Context, cfg *config.HostConfig) {
	eventStore := newEventStore()

	cliRunner := adapters.NewCliRunner(cfg.InfraDirPath, cfg.UseExecLogs)
	stackFS := infrastructure.NewStackFS(cfg.InfraDirPath)
	vhostFS := infrastructure.NewVhostFS(cfg.InfraDirPath)
	layout := infrastructure.NewLayout(cfg.InfraDirPath)
	yamlRepo := adapters.NewYamlStateRepository(cfg.InfraDirPath, sys.FileWriter{})
	acmeBridge := adapters.NewAcmeBridge(newACMEClient(cfg), cfg.InfraDirPath)
	gitSource := adapters.NewGitConfigSource(cfg, eventStore)

	var eventRecorder certmanagement.EventRecorder
	if eventStore != nil {
		eventRecorder = eventStore
	}

	certService := certmanagement.NewCertificateService(
		yamlRepo,
		acmeBridge,
		eventRecorder,
		certmanagement.CertRetryPolicy{
			TriesCount:           cfg.CertsTriesCount,
			BlockDuration:        time.Duration(cfg.CertsBlockHours) * time.Hour,
			DowngradeCertModeTTL: time.Duration(cfg.CertsDowngradeCertModeHours) * time.Hour,
		},
	)
	certIssuer := adapters.NewCertIssuerBridge(certService)

	var dockerEvents docker.EventRecorder
	if eventStore != nil {
		dockerEvents = eventStore
	}

	dockerService := docker.NewService(
		stackFS,
		cliRunner,
		cliRunner,
		dockerEvents,
		docker.Config{ //nolint:exhaustruct_v5 // TrustedImagesPath empty → NewService default $HOME/.trusted_images
			Timeouts: docker.ApplyTimeouts{
				Login:           cfg.DockerLoginTimeout,
				ComposePull:     cfg.DockerComposePullTimeout,
				ComposeUp:       cfg.DockerComposeUpTimeout,
				ComposeDown:     cfg.DockerComposeDownTimeout,
				Prune:           cfg.DockerPruneTimeout,
				API:             cfg.DockerAPITimeout,
				ProxyReadyAfter: cfg.ProxyReadyAfter,
			},
		},
	)

	nginxService := nginx.NewRoutingService(
		vhostFS,
		cliRunner,
		cliRunner,
		certIssuer,
		nginx.ProcessTimeouts{
			CertIssue:      cfg.CertIssueTimeout,
			NginxCLI:       cfg.NginxCLITimeout,
			DomainPrecheck: cfg.DomainPrecheckTimeout,
		},
	)

	gitopsService := deployment.NewGitOpsDeploymentService(
		cfg.Namespace,
		cfg.CertsPublicIP,
		layout,
		gitSource,
		dockerService,
		nginxService,
	)

	err := gitopsService.ExecuteUpdateCycle(ctx)
	if err != nil {
		log.Printf("[update] WARN cycle finished with errors (soft-fail, will retry):\n%v", err)
	}
}

func newEventStore() *events.FileStore {
	path, err := events.DefaultPath()
	if err != nil {
		log.Printf("[update] WARN events path: %v", err)

		return nil
	}

	return events.NewFileStore(path)
}

func newACMEClient(cfg *config.HostConfig) *acme.Client {
	return acme.NewClient(acme.Config{
		InfraDir: cfg.InfraDirPath,
		Provider: cfg.CertsProvider,
		KID:      cfg.AcmeKid,
		HMAC:     cfg.AcmeHmac,
		Directories: acme.Directories{
			LetsEncryptProd:    cfg.LetsEncryptDirectory,
			LetsEncryptStaging: cfg.LetsEncryptStagingDirectory,
			ZeroSSL:            cfg.ZeroSSLDirectory,
		},
		FS:       sys.FileWriter{},
		Accounts: certstore.New(cfg.InfraDirPath),
	})
}
