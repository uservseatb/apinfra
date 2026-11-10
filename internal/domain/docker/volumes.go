package docker

import (
	"errors"
	"fmt"
	"log"
	"strings"

	"github.com/compose-spec/compose-go/v2/types"
)

const (
	allowedDockerSock = "/var/run/docker.sock"

	eventSecurityViolation    = "security_violation"
	eventTrustedImageDeployed = "trusted_image_deployed"

	reasonDisallowedBindMount        = "disallowed_bind_mount"
	reasonUntrustedDockerSocketMount = "untrusted_docker_socket_mount"
	reasonDisallowedVolumeType       = "disallowed_volume_type"

	fieldProject = "project"
	fieldService = "service"
	fieldSource  = "source"
	fieldReason  = "reason"
	fieldImage   = "image"
	fieldDigest  = "digest"
)

var errVolumePolicyViolation = errors.New("volume policy violation")

type trustedSockRef struct {
	service string
	image   string
}

// validateProjectVolumes allows named volumes only; the sole bind exception is
// /var/run/docker.sock for digest-pinned images listed in ~/.trusted_images.
func (s *Service) validateProjectVolumes(projectName string, composeProject *types.Project) error {
	if composeProject == nil {
		return nil
	}

	for serviceName, svc := range composeProject.Services {
		for _, vol := range svc.Volumes {
			err := s.validateServiceVolume(projectName, serviceName, svc.Image, vol)
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
) error {
	source := strings.TrimSpace(vol.Source)

	switch vol.Type {
	case types.VolumeTypeVolume:
		return s.validateNamedVolume(projectName, serviceName, source)
	case types.VolumeTypeBind:
		return s.validateBindVolume(projectName, serviceName, image, source)
	default:
		return s.rejectVolumeType(projectName, serviceName, source, vol.Type)
	}
}

func (s *Service) validateNamedVolume(projectName, serviceName, source string) error {
	if source != "" {
		return nil
	}

	s.recordSecurityViolation(projectName, serviceName, source, reasonDisallowedVolumeType, "")

	return fmt.Errorf(
		"project=%s service=%s: %w: anonymous volumes are forbidden; use a named volume",
		projectName, serviceName, errVolumePolicyViolation,
	)
}

func (s *Service) validateBindVolume(
	projectName, serviceName, image, source string,
) error {
	if source == allowedDockerSock {
		if isImageTrusted(image, s.getTrustedImages()) {
			return nil
		}

		s.recordSecurityViolation(
			projectName, serviceName, source, reasonUntrustedDockerSocketMount, image,
		)

		return fmt.Errorf(
			"project=%s service=%s: %w: %s (image %q not in trusted digest allowlist)",
			projectName, serviceName, errVolumePolicyViolation, source, strings.TrimSpace(image),
		)
	}

	s.recordSecurityViolation(projectName, serviceName, source, reasonDisallowedBindMount, "")

	return fmt.Errorf(
		"project=%s service=%s: %w: source=%q (host bind mounts are forbidden; use named volumes)",
		projectName, serviceName, errVolumePolicyViolation, source,
	)
}

func (s *Service) rejectVolumeType(projectName, serviceName, source, volType string) error {
	typeLabel := volType
	if typeLabel == "" {
		typeLabel = "unknown"
	}

	s.recordSecurityViolation(projectName, serviceName, source, reasonDisallowedVolumeType, "")

	return fmt.Errorf(
		"project=%s service=%s: %w: volume type %q is forbidden "+
			"(only named volumes and trusted docker.sock binds are allowed)",
		projectName, serviceName, errVolumePolicyViolation, typeLabel,
	)
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
			fieldProject: projectName,
			fieldService: ref.service,
			fieldImage:   ref.image,
		}
		if digest != "" {
			fields[fieldDigest] = digest
		}

		writeErr := s.events.Write(eventTrustedImageDeployed, fields)
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
		fieldProject: project,
		fieldService: service,
		fieldSource:  source,
		fieldReason:  reason,
	}
	if strings.TrimSpace(image) != "" {
		fields[fieldImage] = strings.TrimSpace(image)
	}

	writeErr := s.events.Write(eventSecurityViolation, fields)
	if writeErr != nil {
		log.Printf("[docker] WARN write security_violation event: %v", writeErr)
	}
}
