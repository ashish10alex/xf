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

	t.Run("stdout when empty", func(t *testing.T) {
		var buf bytes.Buffer
		if err := writeReport(&buf, "", report, false); err != nil {
			t.Fatalf("writeReport failed: %v", err)
		}
		if got := buf.String(); got != wantMd {
			t.Errorf("writeReport stdout got:\n%q\nwant:\n%q", got, wantMd)
		}
	})

	t.Run("stdout when dash", func(t *testing.T) {
		var buf bytes.Buffer
		if err := writeReport(&buf, "-", report, false); err != nil {
			t.Fatalf("writeReport failed: %v", err)
		}
		if got := buf.String(); got != wantMd {
			t.Errorf("writeReport stdout got:\n%q\nwant:\n%q", got, wantMd)
		}
	})

	t.Run("to file", func(t *testing.T) {
		dir := t.TempDir()
		outPath := filepath.Join(dir, "impact.md")
		var buf bytes.Buffer
		if err := writeReport(&buf, outPath, report, false); err != nil {
			t.Fatalf("writeReport failed: %v", err)
		}
		if buf.Len() != 0 {
			t.Errorf("expected nothing written to w, got: %q", buf.String())
		}
		data, err := os.ReadFile(outPath)
		if err != nil {
			t.Fatalf("reading written file: %v", err)
		}
		if string(data) != wantMd {
			t.Errorf("file content got:\n%q\nwant:\n%q", string(data), wantMd)
		}
	})

	t.Run("JSON to file", func(t *testing.T) {
		outPath := filepath.Join(t.TempDir(), "impact.json")
		var buf bytes.Buffer
		if err := writeReport(&buf, outPath, report, true); err != nil {
			t.Fatalf("writeReport failed: %v", err)
		}
		want, _ := impact.JSON(report)
		if data, err := os.ReadFile(outPath); err != nil || string(data) != string(want) || buf.Len() != 0 {
			t.Errorf("file got %q (%v), w %q; want %q in the file alone", data, err, buf.String(), want)
		}
	})

	t.Run("creates nested directories", func(t *testing.T) {
		dir := t.TempDir()
		outPath := filepath.Join(dir, "sub", "dir", "impact.md")
		var buf bytes.Buffer
		if err := writeReport(&buf, outPath, report, false); err != nil {
			t.Fatalf("writeReport failed: %v", err)
		}
		data, err := os.ReadFile(outPath)
		if err != nil {
			t.Fatalf("reading written file: %v", err)
		}
		if string(data) != wantMd {
			t.Errorf("file content got:\n%q\nwant:\n%q", string(data), wantMd)
		}
	})
}
