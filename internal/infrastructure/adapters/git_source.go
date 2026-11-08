package adapters

import (
	"context"
	"errors"
	"fmt"
	"log"
	"path"
	"strings"
	"time"

	"github.com/uservseatb/apinfra/internal/config"
	"github.com/uservseatb/apinfra/internal/domain/deployment"
	"github.com/uservseatb/apinfra/internal/events"
	"github.com/uservseatb/apinfra/internal/git"
)

const (
	exampleRepoHost = "github.com/example/"
	exampleRepoPath = "example/infra-config"

	sidecarEnvFileName = ".env"
)

var errPlaceholderRepoURL = errors.New("configure repo (still using example placeholder URL)")

// GitConfigSource loads the in-memory config repo and host property files.
type GitConfigSource struct {
	repoURL    string
	repoToken  string
	repoBranch string
	gitTimeout time.Duration
	namespace  string
	envPath    string
	configPath string
	events     *events.FileStore
}

// NewGitConfigSource builds a ConfigSource from host settings.
func NewGitConfigSource(cfg *config.HostConfig, eventStore *events.FileStore) *GitConfigSource {
	return &GitConfigSource{
		repoURL:    cfg.RepoURL,
		repoToken:  cfg.RepoToken,
		repoBranch: cfg.RepoBranch,
		gitTimeout: cfg.GitTimeout,
		namespace:  cfg.Namespace,
		envPath:    cfg.EnvPath,
		configPath: cfg.ConfigPath,
		events:     eventStore,
	}
}

// LoadWorkspace clones the config repo and returns project specs plus host maps.
func (s *GitConfigSource) LoadWorkspace(ctx context.Context, namespace string) (*deployment.Workspace, error) {
	if namespace == "" {
		namespace = s.namespace
	}

	if isPlaceholderRepoURL(s.repoURL) {
		return nil, fmt.Errorf("%w: %s", errPlaceholderRepoURL, s.configPath)
	}

	repo, configs, err := s.cloneAndReadSettings(ctx, namespace)
	if err != nil {
		return nil, err
	}

	hostEnv, err := config.ReadProperties(s.envPath)
	if err != nil {
		return nil, fmt.Errorf("read host env: %w", err)
	}

	hostSettings, err := config.ReadProperties(s.configPath)
	if err != nil {
		return nil, fmt.Errorf("read host config: %w", err)
	}

	projects, err := projectSpecsFromRepo(repo, configs)
	if err != nil {
		return nil, fmt.Errorf("read project files: %w", err)
	}

	return &deployment.Workspace{
		Projects:     projects,
		HostEnv:      hostEnv,
		HostSettings: hostSettings,
	}, nil
}

func (s *GitConfigSource) cloneAndReadSettings(
	ctx context.Context,
	namespace string,
) (*git.Repo, []config.ServiceConfig, error) {
	log.Printf("[repo] config source repo=%s branch=%s", s.repoURL, s.repoBranch)
	log.Printf("[repo] loading in-memory clone (auth=%v)", s.repoToken != "")

	cloneCtx, cancel := context.WithTimeout(ctx, s.gitTimeout)
	defer cancel()

	log.Printf("[repo] clone timeout=%s", s.gitTimeout)

	repo, err := git.Load(cloneCtx, git.LoadOptions{
		URL:    s.repoURL,
		Token:  s.repoToken,
		Branch: s.repoBranch,
		Events: s.events,
	})
	if err != nil {
		return nil, nil, fmt.Errorf("load config repo: %w", err)
	}

	log.Printf("[repo] in-memory clone ready")

	configs, err := config.ReadSettings(repo, namespace)
	if err != nil {
		return nil, nil, fmt.Errorf("read settings: %w", err)
	}

	log.Printf("[update] loaded %d project(s)", len(configs))

	for _, project := range configs {
		log.Printf("[update]   project=%s domain=%s inboundPort=%s entry=%s targetPort=%s",
			project.Name, project.DomainName, project.InboundPort, project.EntryPoint, project.EntryPointPort)
	}

	return repo, configs, nil
}

func projectSpecsFromRepo(repo *git.Repo, configs []config.ServiceConfig) ([]deployment.ProjectSpec, error) {
	projects := make([]deployment.ProjectSpec, 0, len(configs))

	for _, cfg := range configs {
		composeYAML, err := readOptionalFile(repo, path.Join(cfg.NamespacePrefix, cfg.Name, composeFileName))
		if err != nil {
			return nil, fmt.Errorf("project %s: %w", cfg.Name, err)
		}

		sidecarEnvRaw, err := readOptionalFile(repo, path.Join(cfg.NamespacePrefix, cfg.Name, sidecarEnvFileName))
		if err != nil {
			return nil, fmt.Errorf("project %s: %w", cfg.Name, err)
		}

		projects = append(projects, deployment.ProjectSpec{
			Name:              cfg.Name,
			Namespace:         cfg.Namespace,
			NamespacePrefix:   cfg.NamespacePrefix,
			DomainName:        cfg.DomainName,
			InboundPort:       cfg.InboundPort,
			EntryPoint:        cfg.EntryPoint,
			EntryPointPort:    cfg.EntryPointPort,
			CertIssueType:     cfg.CertIssueType,
			CertUpdateSec:     cfg.CertUpdateSec,
			CertAcmeEmail:     cfg.CertAcmeEmail,
			AllowPortsMapping: cfg.AllowPortsMapping,
			ComposeYAML:       composeYAML,
			SidecarEnvRaw:     sidecarEnvRaw,
		})
	}

	return projects, nil
}

// readOptionalFile returns "" when the file is absent; a read failure on an existing file is an error.
func readOptionalFile(repo *git.Repo, name string) (string, error) {
	if !repo.Exists(name) {
		return "", nil
	}

	data, err := repo.ReadFile(name)
	if err != nil {
		return "", fmt.Errorf("read %s: %w", name, err)
	}

	return string(data), nil
}

func isPlaceholderRepoURL(repoURL string) bool {
	normalized := strings.ToLower(strings.TrimSpace(repoURL))

	return strings.Contains(normalized, exampleRepoHost) || strings.Contains(normalized, exampleRepoPath)
}

var _ deployment.ConfigSource = (*GitConfigSource)(nil)
