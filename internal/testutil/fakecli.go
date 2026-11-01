// Package testutil holds helpers shared by xf's tests: stand-ins for the
// dataform and dbt CLIs that run on every OS, and portable temp
// directories and symlinks.
package testutil

import (
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"sync"
	"testing"
)

// fakeNames are the commands package fakecli can stand in for.
var fakeNames = map[string]bool{"dataform": true, "dbt": true, "xf-wait": true, "xf-fail": true, "xf-echo": true, "gcloud": true}

var (
	fakeMu    sync.Mutex
	fakeDir   string
	fakeBuilt string // the fakecli binary, built once per test binary
	fakeBin   = map[string]string{}
)

// FakeCLI returns the path of a stand-in for the named command, one of
// "dataform", "dbt", "xf-wait" (waits for a line on stdin, like a TUI
// waiting for input), "xf-fail" (exits 1 at once), "xf-echo" (prints its arguments as JSON) or
// "gcloud": package fakecli,
// built on first use, under that name. Its behaviour is set with FAKE_*
// environment variables; see fakecli's fakeDataform and fakeDBT.
func FakeCLI(t testing.TB, name string) string {
	t.Helper()
	if !fakeNames[name] {
		t.Fatalf("no fake CLI called %q", name)
	}
	fakeMu.Lock()
	defer fakeMu.Unlock()
	if p, ok := fakeBin[name]; ok {
		return p
	}
	if fakeDir == "" {
		// Shared by every test in the binary, so it outlives any one
		// test's TempDir; the OS cleans it up with the rest of its temp.
		dir, err := os.MkdirTemp("", "xf-fakecli-")
		if err != nil {
			t.Fatal(err)
		}
		fakeDir = dir
	}
	if fakeBuilt == "" {
		bin := filepath.Join(fakeDir, "fakecli")
		if runtime.GOOS == "windows" {
			bin += ".exe"
		}
		gobin := filepath.Join(runtime.GOROOT(), "bin", "go")
		out, err := exec.Command(gobin, "build", "-o", bin, "github.com/ashish10alex/xf/internal/testutil/fakecli").CombinedOutput()
		if err != nil {
			t.Fatalf("building the fake CLIs: %v\n%s", err, out)
		}
		fakeBuilt = bin
	}
	self := fakeBuilt
	p := filepath.Join(fakeDir, name)
	if runtime.GOOS == "windows" {
		p += ".exe"
	}
	if err := os.Link(self, p); err != nil {
		if err := copyFile(self, p); err != nil {
			t.Fatal(err)
		}
	}
	fakeBin[name] = p
	return p
}

func copyFile(src, dst string) error {
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer func() { _ = in.Close() }()
	out, err := os.OpenFile(dst, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, 0o755)
	if err != nil {
		return err
	}
	if _, err := io.Copy(out, in); err != nil {
		_ = out.Close()
		return err
	}
	return out.Close()
}
