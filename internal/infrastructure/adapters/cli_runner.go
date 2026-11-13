package adapters

import (
	"context"
	"errors"
	"fmt"
	"log"
	"net"
	"path/filepath"
	"strings"
	"time"

	"github.com/moby/moby/api/types/container"
	"github.com/moby/moby/client"
	"github.com/uservseatb/apinfra/internal/domain/docker"
	"github.com/uservseatb/apinfra/internal/domain/nginx"
	"github.com/uservseatb/apinfra/internal/sys"
)

var errNilContext = errors.New("nil context")

const (
	proxyContainerName   = "apinfra-proxy"
	composeFileName      = "docker-compose.yml"
	appsDirName          = "apps"
	composeProjectPrefix = "apinfra_"
	composeProjectLabel  = "com.docker.compose.project"
	composeServiceLabel  = "com.docker.compose.service"
)

// CliRunner implements docker CLI/API and nginx proxy/DNS ports via host commands.
type CliRunner struct {
	executor *sys.CommandExecutor
	infraDir string
}

// NewCliRunner builds a CliRunner bound to infraDir.
func NewCliRunner(infraDir string, useExecLogs bool) *CliRunner {
	return &CliRunner{
		executor: &sys.CommandExecutor{WorkDir: infraDir, UseExecLogs: useExecLogs},
		infraDir: infraDir,
	}
}

// ComposePullProxy pulls proxy stack images.
func (r *CliRunner) ComposePullProxy(ctx context.Context, timeout time.Duration) error {
	composePath := filepath.Join(r.infraDir, composeFileName)

	_, _, err := r.execTimed(ctx, timeout, composeCmd(composePath, "", []string{"pull"})...)
	if err != nil {
		return fmt.Errorf("docker compose pull (proxy): %w", err)
	}

	return nil
}

// ComposeUpProxy starts the proxy stack.
func (r *CliRunner) ComposeUpProxy(ctx context.Context, timeout time.Duration) error {
	composePath := filepath.Join(r.infraDir, composeFileName)

	_, _, err := r.execTimed(ctx, timeout, composeCmd(composePath, "", []string{"up", "-d", "--remove-orphans"})...)
	if err != nil {
		return fmt.Errorf("docker compose up (proxy): %w", err)
	}

	return nil
}

// ComposePullApp pulls images for a project stack.
func (r *CliRunner) ComposePullApp(ctx context.Context, projectName string, timeout time.Duration) error {
	composePath := filepath.Join(r.infraDir, appsDirName, projectName, composeFileName)
	projectFlag := composeProjectPrefix + projectName

	_, _, err := r.execTimed(ctx, timeout, composeCmd(composePath, projectFlag, []string{"pull"})...)
	if err != nil {
		return fmt.Errorf("docker compose pull project=%s: %w", projectName, err)
	}

	return nil
}

// ComposeUpApp starts a project stack.
func (r *CliRunner) ComposeUpApp(ctx context.Context, projectName string, timeout time.Duration) error {
	composePath := filepath.Join(r.infraDir, appsDirName, projectName, composeFileName)
	projectFlag := composeProjectPrefix + projectName
	upArgs := composeCmd(composePath, projectFlag, []string{"up", "-d", "--remove-orphans"})

	_, _, err := r.execTimed(ctx, timeout, upArgs...)
	if err != nil {
		return fmt.Errorf("docker compose up project=%s: %w", projectName, err)
	}

	return nil
}

// ComposeDownApp stops a project stack.
func (r *CliRunner) ComposeDownApp(
	ctx context.Context,
	projectName string,
	useComposeFile bool,
	timeout time.Duration,
) error {
	projectFlag := composeProjectPrefix + projectName

	var argv []string

	if useComposeFile {
		composePath := filepath.Join(r.infraDir, appsDirName, projectName, composeFileName)
		argv = composeCmd(composePath, projectFlag, []string{"down"})
	} else {
		argv = append([]string{"docker", "compose", "-p", projectFlag}, "down")
	}

	_, _, err := r.execTimed(ctx, timeout, argv...)
	if err != nil {
		return fmt.Errorf("docker compose down project=%s: %w", projectName, err)
	}

	return nil
}

// RegistryLogin logs into a container registry.
func (r *CliRunner) RegistryLogin(
	ctx context.Context,
	host, user, token string,
	timeout time.Duration,
) error {
	_, _, err := r.execTimedStdin(
		ctx, token, timeout,
		"docker", "login", host, "-u", user, "--password-stdin",
	)
	if err != nil {
		return fmt.Errorf("docker login %s: %w", host, err)
	}

	return nil
}

// PruneImages removes unused docker images.
func (r *CliRunner) PruneImages(ctx context.Context, timeout time.Duration) error {
	_, _, err := r.execTimed(ctx, timeout, "docker", "image", "prune", "-af")
	if err != nil {
		return fmt.Errorf("docker image prune: %w", err)
	}

	return nil
}

// ListContainers returns all containers on the Docker host.
func (r *CliRunner) ListContainers(ctx context.Context) ([]docker.Container, error) {
	cli, err := client.New(client.FromEnv)
	if err != nil {
		return nil, fmt.Errorf("docker client: %w", err)
	}
	defer func() { _ = cli.Close() }()

	//nolint:exhaustruct_v5 // Latest/Since/Before deprecated; Size/Limit/Filters zero
	result, err := cli.ContainerList(ctx, client.ContainerListOptions{All: true})
	if err != nil {
		return nil, fmt.Errorf("container list: %w", err)
	}

	out := make([]docker.Container, 0, len(result.Items))
	for _, item := range result.Items {
		out = append(out, mapDockerContainer(item))
	}

	return out, nil
}

// TestConfiguration runs nginx -t inside apinfra-proxy.
func (r *CliRunner) TestConfiguration(ctx context.Context, timeout time.Duration) error {
	out, errOut, err := r.execTimed(
		ctx, timeout, "docker", "exec", proxyContainerName, "nginx", "-t",
	)
	if err != nil {
		if proxyExecUnavailable(errOut) {
			log.Printf(
				"[nginx] waiting for %s (cannot nginx -t yet; will retry next cycle)",
				proxyContainerName,
			)

			return nginx.ErrProxyNotReady
		}

		return fmt.Errorf("%w: %w (out=%s err=%s)", nginx.ErrNginxTestFailed, err, out, errOut)
	}

	return nil
}

// Reload runs nginx -t then nginx -s reload inside apinfra-proxy.
func (r *CliRunner) Reload(ctx context.Context, timeout time.Duration) error {
	err := r.TestConfiguration(ctx, timeout)
	if err != nil {
		return err
	}

	_, errOut, err := r.execTimed(
		ctx, timeout, "docker", "exec", proxyContainerName, "nginx", "-s", "reload",
	)
	if err != nil {
		if proxyExecUnavailable(errOut) {
			log.Printf(
				"[nginx] waiting for %s (cannot reload yet; will retry next cycle)",
				proxyContainerName,
			)

			return nginx.ErrProxyNotReady
		}

		return fmt.Errorf("nginx reload: %w", err)
	}

	return nil
}

// LookupShortIPs resolves domain via dig +short.
func (r *CliRunner) LookupShortIPs(ctx context.Context, domain string, timeout time.Duration) ([]string, error) {
	if domain == "" {
		return nil, nginx.ErrEmptyDomain
	}

	if strings.ContainsAny(domain, " \t\n;&|$`\"'\\") {
		return nil, fmt.Errorf("%w: unsafe domain %q", nginx.ErrDNSPreflightFailed, domain)
	}

	out, errOut, err := r.execTimed(ctx, timeout, "dig", "+short", "+time=2", "+tries=1", domain)
	if err != nil {
		detail := strings.TrimSpace(errOut)
		if detail == "" {
			detail = err.Error()
		}

		return nil, fmt.Errorf("%w: dig %s: %s", nginx.ErrDNSPreflightFailed, domain, detail)
	}

	return parseDigShortIPs(out), nil
}

func (r *CliRunner) execTimed(ctx context.Context, timeout time.Duration, args ...string) (string, string, error) {
	if ctx == nil {
		return "", "", errNilContext
	}

	err := ctx.Err()
	if err != nil {
		return "", "", fmt.Errorf("exec context: %w", err)
	}

	opCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	out, errOut, execErr := r.executor.Exec(opCtx, args...)
	if execErr != nil {
		return out, errOut, fmt.Errorf("exec: %w", execErr)
	}

	return out, errOut, nil
}

func (r *CliRunner) execTimedStdin(
	ctx context.Context,
	stdin string,
	timeout time.Duration,
	args ...string,
) (string, string, error) {
	if ctx == nil {
		return "", "", errNilContext
	}

	err := ctx.Err()
	if err != nil {
		return "", "", fmt.Errorf("exec context: %w", err)
	}

	opCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	out, errOut, execErr := r.executor.ExecWithStdin(opCtx, stdin, args...)
	if execErr != nil {
		return out, errOut, fmt.Errorf("exec stdin: %w", execErr)
	}

	return out, errOut, nil
}

func composeCmd(composePath, projectFlag string, args []string) []string {
	cmd := []string{"docker", "compose", "-f", composePath}
	if projectFlag != "" {
		cmd = append(cmd, "-p", projectFlag)
	}

	return append(cmd, args...)
}

func proxyExecUnavailable(errOut string) bool {
	lower := strings.ToLower(errOut)

	return strings.Contains(lower, "is restarting") ||
		strings.Contains(lower, "is not running") ||
		strings.Contains(lower, "no such container")
}

func parseDigShortIPs(out string) []string {
	lines := strings.Split(out, "\n")
	ips := make([]string, 0, len(lines))

	for _, line := range lines {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}

		if net.ParseIP(line) == nil {
			continue
		}

		ips = append(ips, line)
	}

	return ips
}

func mapDockerContainer(item container.Summary) docker.Container {
	name := ""
	if len(item.Names) > 0 {
		name = strings.TrimPrefix(item.Names[0], "/")
	}

	status := item.Status

	return docker.Container{
		ID:             item.ID,
		Name:           name,
		Image:          item.Image,
		State:          string(item.State),
		Status:         status,
		UpTime:         upTimeFromStatus(status, item.Created),
		ComposeProject: item.Labels[composeProjectLabel],
		ComposeService: item.Labels[composeServiceLabel],
	}
}

func upTimeFromStatus(status string, createdUnix int64) time.Duration {
	if !strings.HasPrefix(status, "Up") || createdUnix <= 0 {
		return 0
	}

	created := time.Unix(createdUnix, 0)
	if created.IsZero() {
		return 0
	}

	return time.Since(created)
}

// Compile-time checks that CliRunner satisfies the domain ports once they exist.
var (
	_ docker.CLIExecutor     = (*CliRunner)(nil)
	_ docker.ContainerLister = (*CliRunner)(nil)
	_ nginx.ProxyController  = (*CliRunner)(nil)
	_ nginx.DNSResolver      = (*CliRunner)(nil)
)
