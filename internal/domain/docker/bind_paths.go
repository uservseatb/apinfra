package docker

import (
	"context"
	"errors"
	"fmt"
	"path/filepath"
	"strings"

	"github.com/compose-spec/compose-go/v2/types"
)

const (
	metaEnvFile           = ".env"
	metaComposeYML        = "docker-compose.yml"
	metaComposeYAML       = "docker-compose.yaml"
	metaComposeShortYML   = "compose.yml"
	metaComposeShortYAML  = "compose.yaml"
	projectRootBindSource = "."
)

var (
	errEmptyBindSource      = errors.New("empty bind source")
	errAbsoluteBindSource   = errors.New("absolute bind source")
	errProjectRootBind      = errors.New("project root bind source")
	errBindPathTraversal    = errors.New("path traversal in bind source")
	errMetadataBindSource   = errors.New("metadata bind source")
	errBindSourceMissing    = errors.New("bind source missing from config repo")
	errBindSymlinkForbidden = errors.New("symlinks are forbidden in project-local binds")
)

// BindLoad is Git content for project-local bind mounts (files/dirs plus reject lists).
type BindLoad struct {
	Files    map[string][]byte
	Dirs     map[string]struct{}
	Symlinks []string
	Missing  []string
}

// RelativeBindSources returns cleaned relative bind mount sources from compose YAML.
func RelativeBindSources(ctx context.Context, composeYAML, projectName string) ([]string, error) {
	if strings.TrimSpace(composeYAML) == "" {
		return nil, nil
	}

	composeProject, err := loadCompose(ctx, composeYAML, projectName, nil, loadValidate)
	if err != nil {
		return nil, err
	}

	return relativeBindSourcesFromProject(composeProject), nil
}

func relativeBindSourcesFromProject(composeProject *types.Project) []string {
	if composeProject == nil {
		return nil
	}

	seen := map[string]struct{}{}

	var out []string

	for _, svc := range composeProject.Services {
		for _, vol := range svc.Volumes {
			if vol.Type != types.VolumeTypeBind {
				continue
			}

			source := strings.TrimSpace(vol.Source)
			if source == "" || filepath.IsAbs(source) || source == allowedDockerSock {
				continue
			}

			rel, err := normalizeProjectLocalBindSource(source)
			if err != nil {
				rel = strings.TrimSpace(source)
			}

			if rel == "" {
				rel = projectRootBindSource
			}

			if _, ok := seen[rel]; ok {
				continue
			}

			seen[rel] = struct{}{}
			out = append(out, rel)
		}
	}

	return out
}

func normalizeProjectLocalBindSource(source string) (string, error) {
	source = strings.TrimSpace(source)
	if source == "" {
		return "", errEmptyBindSource
	}

	if filepath.IsAbs(source) {
		return "", fmt.Errorf("%w: %q", errAbsoluteBindSource, source)
	}

	if source == projectRootBindSource || source == "./" {
		return "", fmt.Errorf("%w: %q", errProjectRootBind, source)
	}

	cleaned := filepath.Clean(source)
	cleaned = strings.TrimPrefix(cleaned, string(filepath.Separator))

	if cleaned == projectRootBindSource || cleaned == "" {
		return "", fmt.Errorf("%w: %q", errProjectRootBind, source)
	}

	if strings.HasPrefix(cleaned, ".."+string(filepath.Separator)) || cleaned == ".." {
		return "", fmt.Errorf("%w: %q", errBindPathTraversal, source)
	}

	if filepath.IsAbs(cleaned) {
		return "", fmt.Errorf("%w: %q", errAbsoluteBindSource, source)
	}

	base := filepath.Base(cleaned)
	if isBlockedBindMetadata(base) {
		return "", fmt.Errorf("%w: %q", errMetadataBindSource, source)
	}

	return cleaned, nil
}

func isBlockedBindMetadata(base string) bool {
	switch base {
	case metaEnvFile, metaComposeYML, metaComposeYAML, metaComposeShortYML, metaComposeShortYAML:
		return true
	default:
		return false
	}
}

func bindAssetCovers(load BindLoad, rel string) bool {
	if rel == "" || rel == projectRootBindSource {
		return false
	}

	if _, ok := load.Dirs[rel]; ok {
		return true
	}

	if _, ok := load.Files[rel]; ok {
		return true
	}

	prefix := rel + string(filepath.Separator)
	for path := range load.Files {
		if strings.HasPrefix(path, prefix) {
			return true
		}
	}

	return false
}
