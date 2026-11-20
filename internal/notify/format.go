package notify

import (
	"fmt"
	"sort"
	"strings"

	"github.com/uservseatb/apinfra/internal/events"
	"github.com/uservseatb/apinfra/internal/sys"
)

func formatMessage(serverName, namespace string, entry map[string]string) string {
	var b strings.Builder

	b.WriteString(formatAlertTitle(serverName, namespace))

	if value, ok := entry[eventsFieldTS]; ok {
		writeField(&b, eventsFieldTS, value)
	}

	if value, ok := entry[events.FieldProject]; ok {
		writeField(&b, events.FieldProject, value)
	}

	writeField(&b, eventsFieldType, entry[eventsFieldType])

	if value, ok := entry[events.FieldReason]; ok {
		writeField(&b, events.FieldReason, value)
	}

	skip := map[string]struct{}{
		eventsFieldType:     {},
		eventsFieldTS:       {},
		events.FieldProject: {},
		events.FieldReason:  {},
	}

	keys := make([]string, 0, len(entry))
	for key := range entry {
		if _, ok := skip[key]; ok {
			continue
		}

		keys = append(keys, key)
	}

	sort.Strings(keys)

	for _, key := range keys {
		writeField(&b, key, entry[key])
	}

	return b.String()
}

func formatAlertTitle(serverName, namespace string) string {
	serverName = sys.RedactURLUserinfo(strings.TrimSpace(serverName))
	namespace = sys.RedactURLUserinfo(strings.TrimSpace(namespace))

	switch {
	case serverName != "" && namespace != "":
		return serverName + "[" + namespace + "]"
	case serverName != "":
		return serverName
	case namespace != "":
		return "[" + namespace + "]"
	default:
		return "apinfra"
	}
}

func writeField(b *strings.Builder, key, value string) {
	fmt.Fprintf(b, "\n%s: %s", key, sys.RedactURLUserinfo(value))
}

const (
	eventsFieldType = "type"
	eventsFieldTS   = "ts"
)
