package config

import (
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/nicholas-fedor/shoutrrr"
	"github.com/nicholas-fedor/shoutrrr/pkg/types"
	"github.com/uservseatb/apinfra/internal/events"
)

const (
	keyNotificationsEnabled     = "notifications.enabled"
	keyNotificationsTargets     = "notifications.targets"
	keyNotificationsEvents      = "notifications.events"
	keyNotificationsTimeout     = "notifications.timeout"
	keyNotificationsMaxPerCycle = "notifications.maxPerCycle"
	keyNotificationsCutEvery    = "notifications.cutEvery"

	notificationEventsAll = "all"

	defaultNotificationTimeout     = 10 * time.Second
	defaultNotificationMaxPerCycle = 10
	defaultNotificationCutEvery    = 100
)

var (
	errNotificationsTargetsRequired = errors.New("notifications.targets is required when notifications.enabled=true")
	errInvalidNotificationEvent     = errors.New("invalid notifications.events value")
	errInvalidNotificationTarget    = errors.New("invalid notifications.targets URL")
)

// DefaultNotificationEvents is every production event type (empty/`all` filter).
func DefaultNotificationEvents() []string {
	return append([]string(nil), events.AllTypes()...)
}

type hostNotifications struct {
	enabled     bool
	targets     []string
	eventKinds  []string
	timeout     time.Duration
	maxPerCycle int
	cutEvery    int
}

func parseNotifications(envs map[string]string) (hostNotifications, error) {
	enabled := optionalHostBool(envs, keyNotificationsEnabled, false)

	timeout, err := optionalHostDuration(envs, keyNotificationsTimeout, defaultNotificationTimeout)
	if err != nil {
		return hostNotifications{}, fmt.Errorf("%s: %w", keyNotificationsTimeout, err)
	}

	maxPerCycle, err := optionalHostPositiveInt(envs, keyNotificationsMaxPerCycle, defaultNotificationMaxPerCycle)
	if err != nil {
		return hostNotifications{}, fmt.Errorf("%s: %w", keyNotificationsMaxPerCycle, err)
	}

	cutEvery, err := optionalHostPositiveInt(envs, keyNotificationsCutEvery, defaultNotificationCutEvery)
	if err != nil {
		return hostNotifications{}, fmt.Errorf("%s: %w", keyNotificationsCutEvery, err)
	}

	out := hostNotifications{ //nolint:exhaustruct_v5 // targets/eventKinds filled when enabled
		enabled:     enabled,
		timeout:     timeout,
		maxPerCycle: maxPerCycle,
		cutEvery:    cutEvery,
	}

	if !enabled {
		return out, nil
	}

	targets := splitSpaceSeparated(optionalHostKey(envs, keyNotificationsTargets))
	if len(targets) == 0 {
		return hostNotifications{}, errNotificationsTargetsRequired
	}

	//nolint:exhaustruct_v5 // optional HTTPClient/DialContext/Timeout left at library defaults
	sender, err := shoutrrr.CreateSenderWithOptions(types.SenderOptions{}, targets...)
	if err != nil {
		return hostNotifications{}, fmt.Errorf("%w: %w", errInvalidNotificationTarget, err)
	}

	_ = sender.Close()

	out.targets = targets

	eventKinds, err := parseNotificationEvents(optionalHostKey(envs, keyNotificationsEvents))
	if err != nil {
		return hostNotifications{}, err
	}

	out.eventKinds = eventKinds

	return out, nil
}

func parseNotificationEvents(raw string) ([]string, error) {
	parts := splitCommaSeparated(raw)
	if len(parts) == 0 {
		return DefaultNotificationEvents(), nil
	}

	if len(parts) == 1 && parts[0] == notificationEventsAll {
		return DefaultNotificationEvents(), nil
	}

	known := make(map[string]struct{}, len(events.AllTypes()))
	for _, kind := range events.AllTypes() {
		known[kind] = struct{}{}
	}

	out := make([]string, 0, len(parts))
	seen := make(map[string]struct{}, len(parts))

	for _, kind := range parts {
		if kind == notificationEventsAll {
			return nil, fmt.Errorf("%w %q: use alone or omit the key", errInvalidNotificationEvent, kind)
		}

		if _, ok := known[kind]; !ok {
			return nil, fmt.Errorf("%w %q", errInvalidNotificationEvent, kind)
		}

		if _, dup := seen[kind]; dup {
			continue
		}

		seen[kind] = struct{}{}
		out = append(out, kind)
	}

	return out, nil
}

func splitSpaceSeparated(raw string) []string {
	fields := strings.Fields(raw)
	if len(fields) == 0 {
		return nil
	}

	return fields
}

func splitCommaSeparated(raw string) []string {
	if strings.TrimSpace(raw) == "" {
		return nil
	}

	parts := strings.Split(raw, ",")
	out := make([]string, 0, len(parts))

	for _, part := range parts {
		trimmed := strings.TrimSpace(part)
		if trimmed == "" {
			continue
		}

		out = append(out, trimmed)
	}

	return out
}
