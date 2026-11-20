package notify

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/nicholas-fedor/shoutrrr"
	"github.com/nicholas-fedor/shoutrrr/pkg/types"
)

// Sender delivers a formatted notification body to all configured targets.
type Sender interface {
	Send(ctx context.Context, message string) error
}

type shoutrrrSender struct {
	targets []string
}

func newShoutrrrSender(targets []string) *shoutrrrSender {
	return &shoutrrrSender{targets: append([]string(nil), targets...)}
}

func (s *shoutrrrSender) Send(ctx context.Context, message string) error {
	if len(s.targets) == 0 {
		return nil
	}

	//nolint:exhaustruct_v5 // optional HTTPClient/DialContext/Timeout left at library defaults
	sender, err := shoutrrr.CreateSenderWithOptions(types.SenderOptions{}, s.targets...)
	if err != nil {
		return fmt.Errorf("create shoutrrr sender: %w", err)
	}

	defer func() { _ = sender.Close() }()

	errs := sender.SendContext(ctx, message, nil)

	var failed []string

	for i, sendErr := range errs {
		if sendErr == nil {
			continue
		}

		failed = append(failed, fmt.Sprintf("target[%d]: %v", i, sendErr))
	}

	if len(failed) > 0 {
		return fmt.Errorf("%w: %s", errShoutrrrSend, strings.Join(failed, "; "))
	}

	return nil
}

var errShoutrrrSend = errors.New("shoutrrr send")
