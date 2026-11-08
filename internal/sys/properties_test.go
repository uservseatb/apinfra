package sys_test

import (
	"maps"
	"strings"
	"testing"

	"github.com/uservseatb/apinfra/internal/sys"
)

func TestParsePropertiesString(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		content string
		want    map[string]string
	}{
		{name: "empty", content: "", want: map[string]string{}},
		{name: "simple", content: "a=1\nb=2\n", want: map[string]string{"a": "1", "b": "2"}},
		{name: "comments_and_blank_lines_skipped", content: "# header\n\n  # indented\na=1\n", want: map[string]string{"a": "1"}},
		{name: "inline_hash_is_kept", content: "token=abc#def\n", want: map[string]string{"token": "abc#def"}},
		{name: "value_with_equals_sign", content: "url=https://x.test/?a=b\n", want: map[string]string{"url": "https://x.test/?a=b"}},
		{name: "quotes_trimmed", content: "a=\"quoted value\"\n", want: map[string]string{"a": "quoted value"}},
		{name: "whitespace_around_key_and_value", content: "  a  =  1  \n", want: map[string]string{"a": "1"}},
		{name: "empty_value_is_kept", content: "a=\n", want: map[string]string{"a": ""}},
		{name: "line_without_equals_ignored", content: "just-text\na=1\n", want: map[string]string{"a": "1"}},
		{name: "empty_key_ignored", content: "=1\n  = 2\na=3\n", want: map[string]string{"a": "3"}},
		{name: "last_duplicate_wins", content: "a=1\na=2\n", want: map[string]string{"a": "2"}},
		{name: "crlf_line_endings", content: "a=1\r\nb=2\r\n", want: map[string]string{"a": "1", "b": "2"}},
		{name: "dotted_keys_preserved", content: "db.pass=x\n", want: map[string]string{"db.pass": "x"}},
		{name: "no_trailing_newline", content: "a=1", want: map[string]string{"a": "1"}},
	}

	for _, tcase := range tests {
		t.Run(tcase.name, func(t *testing.T) {
			t.Parallel()

			got, err := sys.ParsePropertiesString(tcase.content)
			if err != nil {
				t.Fatalf("ParsePropertiesString: %v", err)
			}

			if !maps.Equal(got, tcase.want) {
				t.Fatalf("got %v, want %v", got, tcase.want)
			}
		})
	}
}

func TestParseProperties_LongLineIsReportedNotTruncated(t *testing.T) {
	t.Parallel()

	long := "a=" + strings.Repeat("x", 128*1024) + "\n"

	_, err := sys.ParseProperties(strings.NewReader(long))
	if err == nil {
		t.Fatal("a line beyond the scanner limit must surface an error instead of silently truncating")
	}
}
