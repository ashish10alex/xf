package main

import (
	"bytes"
	"flag"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/ashish10alex/xf/internal/config"
	"github.com/ashish10alex/xf/internal/impact"
)

func TestImpactHelp(t *testing.T) {
	fs := flag.NewFlagSet("xf impact", flag.ContinueOnError)
	_ = addSettingFlags(fs, config.KeyDataformBin, config.KeyDefaultBranch)
	_ = fs.Bool("pushed", false, "compare the branch pushed to origin (what an API run compiles) instead of the working tree")
	_ = fs.String("fail-on", "", "exit 1 when a change of this `severity` or worse is found: critical or warning")
	output := fs.String("output", "", "write the Markdown to `path` instead of stdout")
	fs.StringVar(output, "o", "", "")

	var b bytes.Buffer
	fs.SetOutput(&b)
	commandUsage(fs, "xf impact [flags]", "test summary")
	fs.Usage()
	help := b.String()

	if !strings.Contains(help, "--output path") {
		t.Errorf("help missing --output path, got:\n%s", help)
	}
	if !strings.Contains(help, "write the Markdown to path instead of stdout") {
		t.Errorf("help missing description for --output, got:\n%s", help)
	}
	if strings.Contains(help, "\n  --o ") || strings.Contains(help, "\n  --o\n") {
		t.Errorf("help should not display alias --o, got:\n%s", help)
	}
}

func TestImpactFlags(t *testing.T) {
	tests := []struct {
		args []string
		want string
	}{
		{[]string{"--output", "impact.md"}, "impact.md"},
		{[]string{"-output", "impact.md"}, "impact.md"},
		{[]string{"--output=impact.md"}, "impact.md"},
		{[]string{"-o", "impact.md"}, "impact.md"},
		{[]string{"-o=impact.md"}, "impact.md"},
		{[]string{}, ""},
	}
	for _, tc := range tests {
		fs := flag.NewFlagSet("xf impact", flag.ContinueOnError)
		_ = addSettingFlags(fs, config.KeyDataformBin, config.KeyDefaultBranch)
		_ = fs.Bool("pushed", false, "")
		_ = fs.String("fail-on", "", "")
		output := fs.String("output", "", "write the Markdown to `path` instead of stdout")
		fs.StringVar(output, "o", "", "")

		if err := fs.Parse(tc.args); err != nil {
			t.Fatalf("Parse(%v) failed: %v", tc.args, err)
		}
		if *output != tc.want {
			t.Errorf("Parse(%v) output = %q, want %q", tc.args, *output, tc.want)
		}
	}
}

func TestWriteReport(t *testing.T) {
	report := &impact.Report{
		Comparison: &impact.Comparison{
			HeadRef:   "feat",
			BaseRef:   "main",
			MergeBase: "abcdef123456",
		},
	}
	wantMd := impact.Markdown(report) + "\n"
	wantJSON, _ := impact.JSON(report)

	t.Run("text on stdout", func(t *testing.T) {
		var buf bytes.Buffer
		if err := writeReport(&buf, "", report, false, false); err != nil {
			t.Fatalf("writeReport failed: %v", err)
		}
		if got, want := buf.String(), impact.Text(report, false); got != want {
			t.Errorf("stdout got:\n%q\nwant:\n%q", got, want)
		}
	})

	t.Run("coloured text on stdout", func(t *testing.T) {
		var buf bytes.Buffer
		if err := writeReport(&buf, "", report, false, true); err != nil {
			t.Fatalf("writeReport failed: %v", err)
		}
		if !strings.Contains(buf.String(), "\x1b[") {
			t.Errorf("no colour in %q", buf.String())
		}
	})

	t.Run("text on stdout and Markdown in the file, in new folders", func(t *testing.T) {
		outPath := filepath.Join(t.TempDir(), "sub", "dir", "impact.md")
		var buf bytes.Buffer
		if err := writeReport(&buf, outPath, report, false, true); err != nil {
			t.Fatalf("writeReport failed: %v", err)
		}
		if got, want := buf.String(), impact.Text(report, true); got != want {
			t.Errorf("stdout got:\n%q\nwant:\n%q", got, want)
		}
		if data, err := os.ReadFile(outPath); err != nil || string(data) != wantMd {
			t.Errorf("file got %q (%v), want %q", data, err, wantMd)
		}
	})

	t.Run("JSON on stdout and in the file", func(t *testing.T) {
		outPath := filepath.Join(t.TempDir(), "impact.json")
		var buf bytes.Buffer
		if err := writeReport(&buf, outPath, report, true, true); err != nil {
			t.Fatalf("writeReport failed: %v", err)
		}
		if data, err := os.ReadFile(outPath); err != nil || string(data) != string(wantJSON) || buf.String() != string(wantJSON) {
			t.Errorf("file got %q (%v), stdout %q; want %q in both", data, err, buf.String(), wantJSON)
		}
	})
}
