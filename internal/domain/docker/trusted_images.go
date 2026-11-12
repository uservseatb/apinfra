package docker

import (
	"bufio"
	"errors"
	"fmt"
	"log"
	"os"
	"strings"
)

const (
	trustedImagesFileName = ".trusted_images"
	sha256DigestMarker    = "@sha256:"
	ownerRWFileMode       = 0o600
)

var errEmptyTrustedImagesPath = errors.New("trusted images path is empty")

func (s *Service) getTrustedImages() map[string]struct{} {
	s.trustedOnce.Do(func() {
		var loadErr error

		s.trustedSet, loadErr = s.loadTrustedImageSet()
		if loadErr != nil {
			log.Printf("[docker] WARN load trusted images: %v (treating as empty allowlist)", loadErr)

			s.trustedSet = map[string]struct{}{}
		}
	})

	return s.trustedSet
}

// loadTrustedImageSet loads ~/.trusted_images (missing → empty set; chmod 0600).
func (s *Service) loadTrustedImageSet() (map[string]struct{}, error) {
	path := strings.TrimSpace(s.trustedImagesPath)
	if path == "" {
		return nil, errEmptyTrustedImagesPath
	}

	return loadTrustedImagesFile(path)
}

func loadTrustedImagesFile(path string) (map[string]struct{}, error) {
	file, err := os.Open(path)
	if err != nil {
		if os.IsNotExist(err) {
			return map[string]struct{}{}, nil
		}

		return nil, fmt.Errorf("open trusted images %s: %w", path, err)
	}
	defer file.Close()

	err = os.Chmod(path, ownerRWFileMode)
	if err != nil {
		return nil, fmt.Errorf("chmod 0600 trusted images %s: %w", path, err)
	}

	out := map[string]struct{}{}
	scanner := bufio.NewScanner(file)

	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}

		out[strings.ToLower(line)] = struct{}{}
	}

	scanErr := scanner.Err()
	if scanErr != nil {
		return nil, fmt.Errorf("read trusted images %s: %w", path, scanErr)
	}

	return out, nil
}

// isImageTrusted is true when image contains @sha256: and matches the allowlist exactly.
func isImageTrusted(image string, trusted map[string]struct{}) bool {
	normalized := strings.ToLower(strings.TrimSpace(image))
	if normalized == "" || !strings.Contains(normalized, sha256DigestMarker) {
		return false
	}

	_, ok := trusted[normalized]

	return ok
}

// imageDigestPart returns "sha256:<hex>" for event fields (drops the leading '@').
func imageDigestPart(image string) string {
	normalized := strings.TrimSpace(image)

	idx := strings.Index(strings.ToLower(normalized), sha256DigestMarker)
	if idx < 0 {
		return ""
	}

	return normalized[idx+len("@"):]
}
