package dbt

import (
	"bufio"
	"bytes"
	"cmp"
	"encoding/json"
	"regexp"
	"slices"
	"strings"

	"github.com/ashish10alex/xf/internal/project"
)

// logEvent is one line of dbt's --log-format json output. dbt-core puts an
// exception's text in data.exc; dbt v2 puts everything in info.msg.
type logEvent struct {
	Info struct {
		Level string `json:"level"`
		Msg   string `json:"msg"`
	} `json:"info"`
	Data struct {
		Exc string `json:"exc"`
	} `json:"data"`
}

var (
	ansi = regexp.MustCompile(`\x1b\[[0-9;]*[A-Za-z]`)
	// dbt v2 ends an error with where it is: "  --> models/a.sql:4:6".
	v2Location = regexp.MustCompile(`(?m)^\s*--> (\S+?)(?::(\d+))?(?::\d+)?\s*$`)
	// dbt-core names the file in parentheses: "Model 'model.p.a' (models/a.sql)".
	coreFile = regexp.MustCompile(`\(([^()\s]+\.(?:sql|yml|yaml|csv|py|md))\)`)
	// The prefixes dbt puts before an error's text.
	errorPrefix = regexp.MustCompile(`^(?:\[error\]\s*|\[ERROR\]:\s*Encountered an error:\s*)`)
)

func stripANSI(s string) string { return ansi.ReplaceAllString(s, "") }

// logErrors returns the errors in dbt's JSON log output, each once, with
// the file they are in when dbt says.
func logErrors(out []byte) []project.CompilationError {
	var errs []project.CompilationError
	sc := bufio.NewScanner(bytes.NewReader(out))
	sc.Buffer(make([]byte, 64*1024), 16*1024*1024)
	for sc.Scan() {
		var ev logEvent
		if json.Unmarshal(sc.Bytes(), &ev) != nil || ev.Info.Level != "error" {
			continue
		}
		e := compilationError(cmp.Or(ev.Data.Exc, ev.Info.Msg))
		if e.Message != "" && !slices.Contains(errs, e) {
			errs = append(errs, e)
		}
	}
	return errs
}

func compilationError(text string) project.CompilationError {
	text = strings.TrimSpace(errorPrefix.ReplaceAllString(strings.TrimSpace(stripANSI(text)), ""))
	var e project.CompilationError
	if m := v2Location.FindStringSubmatchIndex(text); m != nil {
		e.FileName = project.SlashPath(text[m[2]:m[3]])
		if m[4] >= 0 {
			e.Stack = e.FileName + ":" + text[m[4]:m[5]]
		}
		text = strings.TrimSpace(text[:m[0]] + text[m[1]:])
	} else if m := coreFile.FindStringSubmatch(text); m != nil {
		e.FileName = project.SlashPath(m[1])
	}
	e.Message = text
	return e
}

// outputDetail is what dbt printed other than JSON log lines, for when a
// command failed without logging an error: a crash, or a bad flag.
func outputDetail(stdout, stderr []byte) string {
	var lines []string
	for _, out := range [][]byte{stderr, stdout} {
		for line := range strings.SplitSeq(string(out), "\n") {
			line = strings.TrimRight(stripANSI(line), "\r ")
			if line == "" || json.Valid([]byte(line)) {
				continue
			}
			lines = append(lines, line)
		}
	}
	if len(lines) > 40 {
		lines = lines[len(lines)-40:]
	}
	return strings.Join(lines, "\n")
}
