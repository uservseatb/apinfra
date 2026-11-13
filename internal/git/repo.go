package git

import (
	"context"
	"errors"
	"fmt"
	"io"
	"path"
	"strings"

	"github.com/go-git/go-billy/v5"
	"github.com/go-git/go-billy/v5/memfs"
	"github.com/go-git/go-git/v5"
	"github.com/go-git/go-git/v5/plumbing"
	"github.com/go-git/go-git/v5/plumbing/transport"
	"github.com/go-git/go-git/v5/plumbing/transport/http"
	"github.com/go-git/go-git/v5/storage/memory"

	"github.com/uservseatb/apinfra/internal/sys"
)

var (
	errEmptyRepoURL   = errors.New("empty repo URL")
	errBranchNotFound = errors.New("branch not found")
	// errAuth is returned when clone fails due to authentication/authorization.
	errAuth = errors.New("git authentication failed")
)

const eventGitLoginFailed = "git_login_failed"

// eventRecorder records operational events (app passes *events.FileStore).
// git does not import the events package.
type eventRecorder interface {
	Write(kind string, fields map[string]string) error
}

// Repo is an in-memory checkout of a remote config repository.
type Repo struct {
	fs billy.Filesystem
}

// LoadOptions controls a shallow in-memory clone.
type LoadOptions struct {
	URL    string
	Token  string
	Branch string
	Events eventRecorder
}

// Load clones the repository into memory (storage + worktree) and returns a read API.
func Load(ctx context.Context, opt LoadOptions) (*Repo, error) {
	err := ctx.Err()
	if err != nil {
		return nil, fmt.Errorf("context canceled: %w", err)
	}

	url := strings.TrimSpace(opt.URL)
	if url == "" {
		return nil, errEmptyRepoURL
	}

	branch := strings.TrimSpace(opt.Branch)
	if branch == "" {
		branch = "main"
	}

	storer := memory.NewStorage()
	worktree := memfs.New()

	cloneOpts := &git.CloneOptions{
		URL:               url,
		Auth:              authFor(url, opt.Token),
		RemoteName:        "",
		ReferenceName:     plumbing.NewBranchReferenceName(branch),
		SingleBranch:      true,
		Mirror:            false,
		NoCheckout:        false,
		Depth:             1,
		RecurseSubmodules: 0,
		ShallowSubmodules: false,
		Progress:          nil,
		Tags:              git.NoTags,
		InsecureSkipTLS:   false,
		ClientCert:        nil,
		ClientKey:         nil,
		CABundle:          nil,
		ProxyOptions: transport.ProxyOptions{
			URL:      "",
			Username: "",
			Password: "",
		},
		Shared: false,
	}

	_, err = git.CloneContext(ctx, storer, worktree, cloneOpts)
	if err != nil {
		safeURL := sys.RedactURLUserinfo(url)
		safeErr := sys.RedactURLUserinfo(err.Error())

		switch {
		case isBranchNotFound(err, branch):
			return nil, fmt.Errorf("%w: %q in %s", errBranchNotFound, branch, safeURL)
		case isAuthError(err):
			recordGitLoginFailed(opt.Events, safeURL)

			return nil, fmt.Errorf("%w: clone %s: %s", errAuth, safeURL, safeErr)
		default:
			return nil, fmt.Errorf("clone %s: %s", safeURL, safeErr)
		}
	}

	return &Repo{fs: worktree}, nil
}

// ReadFile reads a file relative to the repo root (forward-slash paths).
func (r *Repo) ReadFile(name string) ([]byte, error) {
	name = cleanPath(name)

	file, err := r.fs.Open(name)
	if err != nil {
		return nil, fmt.Errorf("open %s: %w", name, err)
	}

	defer func() { _ = file.Close() }()

	data, err := io.ReadAll(file)
	if err != nil {
		return nil, fmt.Errorf("read %s: %w", name, err)
	}

	return data, nil
}

// Exists reports whether path exists in the in-memory worktree.
func (r *Repo) Exists(name string) bool {
	name = cleanPath(name)
	_, err := r.fs.Stat(name)

	return err == nil
}

func recordGitLoginFailed(recorder eventRecorder, repoURL string) {
	if recorder == nil {
		return
	}

	_ = recorder.Write(eventGitLoginFailed, map[string]string{"repo": repoURL})
}

func isAuthError(err error) bool {
	if err == nil {
		return false
	}

	return errors.Is(err, transport.ErrAuthenticationRequired) ||
		errors.Is(err, transport.ErrAuthorizationFailed) ||
		errors.Is(err, transport.ErrInvalidAuthMethod)
}

func isBranchNotFound(err error, branch string) bool {
	if err == nil {
		return false
	}

	if errors.Is(err, plumbing.ErrReferenceNotFound) {
		return true
	}

	msg := strings.ToLower(err.Error())
	ref := strings.ToLower("refs/heads/" + branch)

	return (strings.Contains(msg, "couldn't find remote ref") ||
		strings.Contains(msg, "reference not found")) &&
		strings.Contains(msg, ref)
}

func authFor(repoURL, token string) *http.BasicAuth {
	token = strings.TrimSpace(token)
	if token == "" {
		return nil
	}

	switch {
	case strings.HasPrefix(repoURL, "file://"), strings.HasPrefix(repoURL, "git://"):
		return nil
	case strings.HasPrefix(repoURL, "http://"), strings.HasPrefix(repoURL, "https://"):
		// GitHub / GitLab HTTPS tokens accept x-access-token as username.
		return &http.BasicAuth{Username: "x-access-token", Password: token}
	default:
		return nil
	}
}

func cleanPath(name string) string {
	name = strings.TrimPrefix(path.Clean("/"+strings.ReplaceAll(name, "\\", "/")), "/")
	if name == "." {
		return ""
	}

	return name
}
