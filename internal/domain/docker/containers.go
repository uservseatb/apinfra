package docker

import (
	"slices"
	"time"
)

const containerStateRunning = "running"

// serviceDeployDiff classifies compose services for one project relative to the
// host ContainerList taken before compose up.
type serviceDeployDiff struct {
	containers []string // desired compose service keys
	added      []string // desired, no container on host yet
	removed    []string // on host for this compose project, not in desired (orphans)
	started    []string // desired, present on host but not running
}

func computeServiceDeployDiff(
	containers []Container,
	composeProject string,
	desired map[string]struct{},
) serviceDeployDiff {
	type svcState struct {
		present bool
		running bool
	}

	observed := make(map[string]*svcState)

	for _, c := range containers {
		if c.ComposeProject != composeProject || c.ComposeService == "" {
			continue
		}

		state := observed[c.ComposeService]
		if state == nil {
			state = &svcState{present: false, running: false}
			observed[c.ComposeService] = state
		}

		state.present = true
		if c.State == containerStateRunning {
			state.running = true
		}
	}

	desiredNames := sortedSetNames(desired)

	var added, started []string

	for _, name := range desiredNames {
		state := observed[name]
		switch {
		case state == nil:
			added = append(added, name)
		case !state.running:
			started = append(started, name)
		}
	}

	var removed []string

	for name := range observed {
		if _, ok := desired[name]; ok {
			continue
		}

		removed = append(removed, name)
	}

	slices.Sort(removed)

	return serviceDeployDiff{
		containers: desiredNames,
		added:      added,
		removed:    removed,
		started:    started,
	}
}

func sortedSetNames(set map[string]struct{}) []string {
	if len(set) == 0 {
		return nil
	}

	out := make([]string, 0, len(set))
	for name := range set {
		out = append(out, name)
	}

	slices.Sort(out)

	return out
}

func proxyReadyAmong(containers []Container, readyAfter time.Duration) bool {
	if readyAfter <= 0 {
		readyAfter = defaultProxyReadyAfter
	}

	for _, c := range containers {
		if c.Name == proxyContainerName &&
			c.State == containerStateRunning &&
			c.UpTime >= readyAfter {
			return true
		}
	}

	return false
}

// runningComposeProjects returns compose project names (-p) that match desired
// services and are fully running.
//
// A project is running only when every desired YAML service has at least one
// container in Docker state "running", and no compose-managed container for that
// project is non-running. Deleted services (absent from ContainerList) omit the
// project so compose up can recreate them; stopped containers do the same.
//
// desiredServices maps compose -p name → set of YAML service keys from the
// stack that was just written for this cycle.
func runningComposeProjects(
	containers []Container,
	desiredServices map[string]map[string]struct{},
) map[string]struct{} {
	type projectState struct {
		runningServices map[string]struct{}
		hasNonRunning   bool
	}

	states := make(map[string]*projectState)

	for _, c := range containers {
		if c.ComposeProject == "" {
			continue
		}

		state := states[c.ComposeProject]
		if state == nil {
			state = &projectState{ //nolint:exhaustruct_v5 // hasNonRunning starts false
				runningServices: make(map[string]struct{}),
			}
			states[c.ComposeProject] = state
		}

		if c.State != containerStateRunning {
			state.hasNonRunning = true

			continue
		}

		if c.ComposeService != "" {
			state.runningServices[c.ComposeService] = struct{}{}
		}
	}

	out := make(map[string]struct{})

	for project, want := range desiredServices {
		if len(want) == 0 {
			continue
		}

		state := states[project]
		if state == nil || state.hasNonRunning {
			continue
		}

		if !allDesiredServicesRunning(want, state.runningServices) {
			continue
		}

		out[project] = struct{}{}
	}

	return out
}

func allDesiredServicesRunning(want, running map[string]struct{}) bool {
	for svc := range want {
		if _, ok := running[svc]; !ok {
			return false
		}
	}

	return true
}

func proxyRunningAmong(containers []Container) bool {
	for _, c := range containers {
		if c.Name == proxyContainerName && c.State == containerStateRunning {
			return true
		}
	}

	return false
}
