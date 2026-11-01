package web

import (
	"reflect"
	"testing"
)

func TestColorLine(t *testing.T) {
	for _, tt := range []struct {
		name, in string
		want     logLine
	}{
		{"plain", "Compiling...", logLine{Text: "Compiling..."}},
		{"dbt", "\x1b[32m\x1b[1m   Loading\x1b[0m profiles.yml", logLine{Text: "   Loading profiles.yml",
			Spans: []logSpan{{Text: "   Loading", Class: "a-green a-b"}, {Text: " profiles.yml"}}}},
		{"one code, many params", "\x1b[1;31mFAIL\x1b[22m x\x1b[m y", logLine{Text: "FAIL x y",
			Spans: []logSpan{{Text: "FAIL", Class: "a-red a-b"}, {Text: " x", Class: "a-red"}, {Text: " y"}}}},
		{"bright", "\x1b[92mok\x1b[39m", logLine{Text: "ok", Spans: []logSpan{{Text: "ok", Class: "a-green"}}}},
		// White and 256-colour text would leave the theme: they show plain.
		{"white and extended", "\x1b[37mdev\x1b[0m \x1b[38;5;196mhot\x1b[0m", logLine{Text: "dev hot"}},
		{"extended keeps what follows", "\x1b[38;2;1;2;3;4mx", logLine{Text: "x", Spans: []logSpan{{Text: "x", Class: "a-u"}}}},
		{"redraw", "  1/3 \r  2/3 \r\x1b[2m  3/3\x1b[0m", logLine{Text: "  3/3", Spans: []logSpan{{Text: "  3/3", Class: "a-d"}}}},
		{"redraw keeps the colour", "\x1b[2m  1/3\r  3/3\x1b[0m", logLine{Text: "  3/3", Spans: []logSpan{{Text: "  3/3", Class: "a-d"}}}},
		{"redraw drops the colour it covers", "\x1b[31mx\x1b[0m\rdone", logLine{Text: "done"}},
		{"other escapes go", "\x1b[2Kdone\x1b]8;;http://x\x07", logLine{Text: "done"}},
	} {
		if got := colorLine(tt.in); !reflect.DeepEqual(got, tt.want) {
			t.Errorf("%s: colorLine(%q) = %+v, want %+v", tt.name, tt.in, got, tt.want)
		}
	}
}
