package docker

import "time"

// proxyReadyAfter is how long apinfra-proxy must be up before cert issuance is safe.
const proxyReadyAfter = 30 * time.Second

const containerStateRunning = "running"

func proxyReadyAmong(containers []Container) bool {
	for _, c := range containers {
		if c.Name == proxyContainerName &&
			c.State == containerStateRunning &&
			c.UpTime >= proxyReadyAfter {
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
