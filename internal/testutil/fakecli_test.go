package testutil

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func TestFakeDataformCompile(t *testing.T) {
	fixture := filepath.Join(t.TempDir(), "out.json")
	if err := os.WriteFile(fixture, []byte(`{"tables": []}`), 0o644); err != nil {
		t.Fatal(err)
	}
	t.Setenv("FAKE_COMPILE_FIXTURE", fixture)
	t.Setenv("FAKE_COMPILE_EXIT", "3")
	out, err := exec.Command(FakeCLI(t, "dataform"), "compile", "--json").Output()
	if code := exitCode(err); code != 3 || string(out) != `{"tables": []}` {
		t.Errorf("out %q, exit %d", out, code)
	}
}

func TestFakeDBTVersionAndManifest(t *testing.T) {
	bin := FakeCLI(t, "dbt")
	if out, err := exec.Command(bin, "--version").Output(); err != nil || strings.TrimSpace(string(out)) != "dbt 2.0.6" {
		t.Errorf("--version: %q, %v", out, err)
	}
	manifest := filepath.Join(t.TempDir(), "m.json")
	if err := os.WriteFile(manifest, []byte("{}"), 0o644); err != nil {
		t.Fatal(err)
	}
	calls, target := filepath.Join(t.TempDir(), "calls"), filepath.Join(t.TempDir(), "t")
	t.Setenv("FAKE_DBT_PARSE", manifest)
	t.Setenv("FAKE_DBT_CALLS", calls)
	if err := exec.Command(bin, "parse", "--target-path", target, "--target", "ci").Run(); err != nil {
		t.Fatal(err)
	}
	if got, _ := os.ReadFile(calls); strings.TrimSpace(string(got)) != "parse:ci" {
		t.Errorf("calls = %q", got)
	}
	if _, err := os.Stat(filepath.Join(target, "manifest.json")); err != nil {
		t.Error(err)
	}
}

func TestFakeWaitAndFail(t *testing.T) {
	wait := exec.Command(FakeCLI(t, "xf-wait"))
	wait.Stdin = strings.NewReader("anything")
	if err := wait.Run(); err != nil {
		t.Errorf("xf-wait: %v", err)
	}
	if code := exitCode(exec.Command(FakeCLI(t, "xf-fail")).Run()); code != 1 {
		t.Errorf("xf-fail exit %d", code)
	}
}

func exitCode(err error) int {
	if e, ok := err.(*exec.ExitError); ok {
		return e.ExitCode()
	}
	if err != nil {
		return -1
	}
	return 0
}
