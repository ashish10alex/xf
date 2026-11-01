package main

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/ashish10alex/xf/internal/backend"
	"github.com/ashish10alex/xf/internal/backend/dataform"
	"github.com/ashish10alex/xf/internal/remote"
	"github.com/ashish10alex/xf/internal/testutil"
)

// xf serve runs from a copy of xf, so xf.exe can be replaced while the
// engine runs, and that copy removes copies of older builds.
func TestServeRunsFromACopy(t *testing.T) {
	tmp := t.TempDir()
	exe := filepath.Join(tmp, "install", "xf.exe")
	// go test puts its own go first on PATH.
	out, err := exec.Command("go", "build", "-o", exe, ".").CombinedOutput()
	if err != nil {
		t.Fatalf("building xf: %v\n%s", err, out)
	}
	local := filepath.Join(tmp, "local")
	binDir := filepath.Join(local, "xf", "bin")
	stale := filepath.Join(binDir, engineCopyPrefix+"0000000000000000.exe")
	if err := os.MkdirAll(binDir, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(stale, []byte("an older build"), 0o700); err != nil {
		t.Fatal(err)
	}

	root, err := filepath.Abs(filepath.Join("..", "..", "examples", "dataform"))
	if err != nil {
		t.Fatal(err)
	}
	b := dataform.Backend{Bin: testutil.FakeCLI(t, "dataform")}
	stub := exec.Command(exe, serveArgs(root, b)...)
	stub.Dir = root
	stub.Env = append(os.Environ(), "LOCALAPPDATA="+local)
	if stub, err = startDetached(stub); err != nil {
		t.Fatal(err)
	}
	exited := make(chan error, 1)
	go func() { exited <- stub.Wait() }()
	select {
	case err := <-exited:
		if err != nil {
			t.Fatalf("xf serve: %v", err)
		}
	case <-time.After(30 * time.Second):
		_ = stub.Process.Kill()
		t.Fatal("xf serve waited for its copy")
	}

	// Hold a connection, so the engine doesn't stop for want of clients.
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	conn, err := remote.Dialer(remote.SocketPath(root, backend.EngineName(b)), func() error { return nil })(ctx)
	if err != nil {
		t.Fatalf("the engine isn't listening: %v", err)
	}
	defer func() { _ = conn.Close() }()

	procs, err := listProcs()
	if err != nil {
		t.Fatal(err)
	}
	i := slices.IndexFunc(procs, func(p proc) bool {
		return strings.EqualFold(filepath.Dir(p.Path), binDir) && xfRole(p, binDir) == roleServe
	})
	if i < 0 {
		t.Fatalf("no engine runs from %s", binDir)
	}
	engine := procs[i].PID
	defer func() { _ = stopProc(engine) }()

	// What an upgrade does: write the new xf.exe over the old.
	if err := os.WriteFile(exe, []byte("a newer build"), 0o755); err != nil {
		t.Errorf("replacing xf.exe while the engine runs: %v", err)
	}
	if _, err := os.Stat(stale); !os.IsNotExist(err) {
		t.Errorf("the older build's copy is still there: %v", err)
	}
}
