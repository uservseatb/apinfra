package docker

import (
	"context"
	"errors"
	"fmt"
	"maps"

	"github.com/compose-spec/compose-go/v2/loader"
	"github.com/compose-spec/compose-go/v2/types"
)

var errNilComposeProject = errors.New("nil compose project")

const composeFilename = "docker-compose.yml"

// loadMode selects loader options for validation vs mutate-and-write.
type loadMode int

const (
	// loadValidate runs schema + consistency checks with env interpolation.
	loadValidate loadMode = iota
	// loadMutate loads a typed model for in-memory edits. Skips normalization so
	// marshal does not invent a default network or rewrite network names.
	loadMutate
)

// loadCompose parses compose YAML via compose-go into a typed project.
// env is used only for interpolation (no OS env). ResolvePaths is always off.
func loadCompose(
	ctx context.Context,
	raw, projectName string,
	env map[string]string,
	mode loadMode,
) (*types.Project, error) {
	if ctx == nil {
		return nil, errNilContext
	}

	name := loader.NormalizeProjectName(projectName)
	if name == "" {
		name = "project"
	}

	environment := types.Mapping{}
	if env != nil {
		maps.Copy(environment, env)
	}

	//nolint:exhaustruct_v5 // Version unused; Config filled by loader from Content
	details := types.ConfigDetails{
		WorkingDir: ".",
		ConfigFiles: []types.ConfigFile{{
			Filename: composeFilename,
			Content:  []byte(raw),
		}},
		Environment: environment,
	}

	project, err := loader.LoadWithContext(ctx, details, func(opts *loader.Options) {
		opts.SetProjectName(name, true)
		opts.ResolvePaths = false
		// Config YAML arrives from git as a single blob; never pull sibling
		// host files via include/extends (WorkingDir would be process cwd).
		opts.SkipInclude = true
		opts.SkipExtends = true

		if mode == loadMutate {
			opts.SkipNormalization = true
			opts.SkipConsistencyCheck = true
		}
	})
	if err != nil {
		return nil, fmt.Errorf("load compose: %w", err)
	}

	return project, nil
}

// marshalCompose encodes a compose project as YAML for the host infra tree.
// Clears the project name so `docker compose -p` owns naming without a file-level name:.
func marshalCompose(project *types.Project) (string, error) {
	if project == nil {
		return "", errNilComposeProject
	}

	saved := project.Name
	project.Name = ""

	defer func() { project.Name = saved }()

	out, err := project.MarshalYAML()
	if err != nil {
		return "", fmt.Errorf("marshal compose YAML: %w", err)
	}

	return string(out), nil
}
