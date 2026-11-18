package docker

import (
	"errors"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"strings"

	"github.com/compose-spec/compose-go/v2/types"
	"github.com/uservseatb/apinfra/internal/events"
)

const allowedDockerSock = "/var/run/docker.sock"

var errVolumePolicyViolation = errors.New("volume policy violation")

type trustedSockRef struct {
	service string
	image   string
}

// validateProjectVolumes allows named volumes, trusted docker.sock binds, and
// project-local relative binds covered by load (no symlinks / missing sources).
func (s *Service) validateProjectVolumes(
	projectName string,
	composeProject *types.Project,
	load BindLoad,
) error {
	if composeProject == nil {
		return nil
	}

	for _, path := range load.Symlinks {
		s.recordSecurityViolation(projectName, "", path, events.ReasonDisallowedBindMount, "")

		return fmt.Errorf(
			"project=%s: %w: source=%q: %w",
			projectName, errVolumePolicyViolation, path, errBindSymlinkForbidden,
		)
	}

	for _, path := range load.Missing {
		s.recordSecurityViolation(projectName, "", path, events.ReasonDisallowedBindMount, "")

		return fmt.Errorf(
			"project=%s: %w: source=%q: %w",
			projectName, errVolumePolicyViolation, path, errBindSourceMissing,
		)
	}

	for serviceName, svc := range composeProject.Services {
		for _, vol := range svc.Volumes {
			err := s.validateServiceVolume(projectName, serviceName, svc.Image, vol, load)
			if err != nil {
				return err
			}
		}
	}

	return nil
}

func (s *Service) validateServiceVolume(
	projectName, serviceName, image string,
	vol types.ServiceVolumeConfig,
	load BindLoad,
) error {
	source := strings.TrimSpace(vol.Source)

	switch vol.Type {
	case types.VolumeTypeVolume:
		return s.validateNamedVolume(projectName, serviceName, source)
	case types.VolumeTypeBind:
		return s.validateBindVolume(projectName, serviceName, image, source, load)
	default:
		return s.rejectVolumeType(projectName, serviceName, source, vol.Type)
	}
}

func (s *Service) validateNamedVolume(projectName, serviceName, source string) error {
	if source != "" {
		return nil
	}

	s.recordSecurityViolation(projectName, serviceName, source, events.ReasonDisallowedVolumeType, "")

	return fmt.Errorf(
		"project=%s service=%s: %w: anonymous volumes are forbidden; use a named volume",
		projectName, serviceName, errVolumePolicyViolation,
	)
}

func (s *Service) validateBindVolume(
	projectName, serviceName, image, source string,
	load BindLoad,
) error {
	if source == allowedDockerSock {
		if isImageTrusted(image, s.getTrustedImages()) {
			return nil
		}

		s.recordSecurityViolation(
			projectName, serviceName, source, events.ReasonUntrustedDockerSocketMount, image,
		)

		return fmt.Errorf(
			"project=%s service=%s: %w: %s (image %q not in trusted digest allowlist)",
			projectName, serviceName, errVolumePolicyViolation, source, strings.TrimSpace(image),
		)
	}

	if filepath.IsAbs(source) {
		s.recordSecurityViolation(projectName, serviceName, source, events.ReasonDisallowedBindMount, "")

		return fmt.Errorf(
			"project=%s service=%s: %w: source=%q (host bind mounts are forbidden; use named volumes)",
			projectName, serviceName, errVolumePolicyViolation, source,
		)
	}

	rel, err := normalizeProjectLocalBindSource(source)
	if err != nil {
		s.recordSecurityViolation(projectName, serviceName, source, events.ReasonDisallowedBindMount, "")

		return fmt.Errorf(
			"project=%s service=%s: %w: source=%q: %w",
			projectName, serviceName, errVolumePolicyViolation, source, err,
		)
	}

	if !bindAssetCovers(load, rel) {
		s.recordSecurityViolation(projectName, serviceName, source, events.ReasonDisallowedBindMount, "")

		return fmt.Errorf(
			"project=%s service=%s: %w: source=%q: %w",
			projectName, serviceName, errVolumePolicyViolation, source, errBindSourceMissing,
		)
	}

	return nil
}

func (s *Service) rejectVolumeType(projectName, serviceName, source, volType string) error {
	typeLabel := volType
	if typeLabel == "" {
		typeLabel = "unknown"
	}

	s.recordSecurityViolation(projectName, serviceName, source, events.ReasonDisallowedVolumeType, "")

	return fmt.Errorf(
		"project=%s service=%s: %w: volume type %q is forbidden "+
			"(only named volumes, project-local read-only binds, and trusted docker.sock binds are allowed)",
		projectName, serviceName, errVolumePolicyViolation, typeLabel,
	)
}

// enforceProjectLocalBinds rewrites allowed relative binds to absolute paths under
// projectDir and forces read_only=true (overrides :rw / read_only: false).
func enforceProjectLocalBinds(composeProject *types.Project, projectDir string) {
	if composeProject == nil {
		return
	}

	for name, svc := range composeProject.Services {
		changed := false

		for i := range svc.Volumes {
			vol := &svc.Volumes[i]
			if vol.Type != types.VolumeTypeBind {
				continue
			}

			source := strings.TrimSpace(vol.Source)
			if source == allowedDockerSock || filepath.IsAbs(source) {
				continue
			}

			rel, err := normalizeProjectLocalBindSource(source)
			if err != nil {
				continue
			}

			vol.Source = filepath.Join(projectDir, rel)
			vol.ReadOnly = true
			changed = true
		}

		if changed {
			composeProject.Services[name] = svc
		}
	}
}

func verifyHostBindSources(projectDir string, composeProject *types.Project) error {
	if composeProject == nil {
		return nil
	}

	for _, svc := range composeProject.Services {
		for _, vol := range svc.Volumes {
			if vol.Type != types.VolumeTypeBind {
				continue
			}

			source := strings.TrimSpace(vol.Source)
			if source == allowedDockerSock || !filepath.IsAbs(source) {
				continue
			}

			if !strings.HasPrefix(source, projectDir+string(filepath.Separator)) && source != projectDir {
				continue
			}

			err := rejectSymlinksInHostPath(source)
			if err != nil {
				return err
			}

			_, err = os.Lstat(source)
			if err != nil {
				return fmt.Errorf("bind source %q: %w", source, err)
			}
		}
	}

	return nil
}

func rejectSymlinksInHostPath(root string) error {
	info, err := os.Lstat(root)
	if err != nil {
		return fmt.Errorf("lstat %s: %w", root, err)
	}

	if info.Mode()&os.ModeSymlink != 0 {
		return fmt.Errorf("%w: %s", errBindSymlinkForbidden, root)
	}

	if !info.IsDir() {
		return nil
	}

	err = filepath.Walk(root, func(path string, _ os.FileInfo, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}

		entry, lerr := os.Lstat(path)
		if lerr != nil {
			return fmt.Errorf("lstat %s: %w", path, lerr)
		}

		if entry.Mode()&os.ModeSymlink != 0 {
			return fmt.Errorf("%w: %s", errBindSymlinkForbidden, path)
		}

		return nil
	})
	if err != nil {
		return fmt.Errorf("walk bind source %s: %w", root, err)
	}

	return nil
}

func collectTrustedSockRefs(composeProject *types.Project) []trustedSockRef {
	if composeProject == nil {
		return nil
	}

	var out []trustedSockRef

	for serviceName, svc := range composeProject.Services {
		if !serviceHasDockerSock(svc) {
			continue
		}

		out = append(out, trustedSockRef{
			service: serviceName,
			image:   strings.TrimSpace(svc.Image),
		})
	}

	return out
}

func serviceHasDockerSock(svc types.ServiceConfig) bool {
	for _, vol := range svc.Volumes {
		if vol.Type == types.VolumeTypeBind && strings.TrimSpace(vol.Source) == allowedDockerSock {
			return true
		}
	}

	return false
}

func (s *Service) rememberTrustedSocks(projectName string, composeProject *types.Project) {
	refs := collectTrustedSockRefs(composeProject)
	if len(refs) == 0 {
		return
	}

	if s.trustedSocks == nil {
		s.trustedSocks = map[string][]trustedSockRef{}
	}

	s.trustedSocks[projectName] = refs
}

func (s *Service) emitTrustedImageDeployed(projectName string) {
	refs := s.trustedSocks[projectName]
	if len(refs) == 0 {
		return
	}

	for _, ref := range refs {
		digest := imageDigestPart(ref.image)
		log.Printf(
			"[docker] trusted image deployed project=%s service=%s image=%s",
			projectName, ref.service, ref.image,
		)

		if s.events == nil {
			continue
		}

		fields := map[string]string{
			events.FieldProject: projectName,
			events.FieldService: ref.service,
			events.FieldImage:   ref.image,
		}
		if digest != "" {
			fields[events.FieldDigest] = digest
		}

		writeErr := s.events.AddEvent(events.TrustedImageDeployed, fields)
		if writeErr != nil {
			log.Printf("[docker] WARN write trusted-image-deployed event: %v", writeErr)
		}
	}
}

func (s *Service) recordSecurityViolation(project, service, source, reason, image string) {
	log.Printf(
		"[docker] security_violation project=%s service=%s source=%s reason=%s image=%q",
		project, service, source, reason, image,
	)

	if s.events == nil {
		return
	}

	fields := map[string]string{
		events.FieldProject: project,
		events.FieldService: service,
		events.FieldSource:  source,
		events.FieldReason:  reason,
	}
	if strings.TrimSpace(image) != "" {
		fields[events.FieldImage] = strings.TrimSpace(image)
	}

	writeErr := s.events.AddEvent(events.SecurityViolation, fields)
	if writeErr != nil {
		log.Printf("[docker] WARN write security_violation event: %v", writeErr)
	}
}
