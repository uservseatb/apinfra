package infrastructure

import (
	"fmt"

	"github.com/uservseatb/apinfra/internal/domain/deployment"
	"github.com/uservseatb/apinfra/internal/sys"
)

// Layout ensures the host infra directory tree exists.
type Layout struct {
	infraDir string
}

// NewLayout builds a Layout for infraDir.
func NewLayout(infraDir string) *Layout {
	return &Layout{infraDir: infraDir}
}

// EnsureLayout creates nginx/certbot/apps directories under infra.
func (l *Layout) EnsureLayout() error {
	err := sys.EnsureInfraLayout(l.infraDir)
	if err != nil {
		return fmt.Errorf("ensure infra layout: %w", err)
	}

	return nil
}

var _ deployment.LayoutInitializer = (*Layout)(nil)
