package web

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// TestJS runs the page's JavaScript tests (jstest/*.test.js) with node, so
// go test ./... checks the page too. It is skipped where node isn't on PATH.
func TestJS(t *testing.T) {
	node, err := exec.LookPath("node")
	if err != nil {
		t.Skip("node is not on PATH")
	}
	// go test's cache only knows the files this process opens: reading
	// every file node will read makes a change to one run the tests again.
	var tests []string
	for _, dir := range []string{"assets", "jstest"} {
		err := filepath.WalkDir(dir, func(path string, d os.DirEntry, err error) error {
			if err != nil || d.IsDir() || !strings.HasSuffix(path, "js") {
				return err
			}
			if _, err := os.ReadFile(path); err != nil {
				return err
			}
			if strings.HasSuffix(path, ".test.js") {
				tests = append(tests, path)
			}
			return nil
		})
		if err != nil {
			t.Fatal(err)
		}
	}

	ctx, cancel := context.WithTimeout(t.Context(), 2*time.Minute)
	defer cancel()
	args := append([]string{"--import", "./jstest/setup.js", "--test"}, tests...)
	out, err := exec.CommandContext(ctx, node, args...).CombinedOutput()
	if err != nil {
		t.Fatalf("node --test: %v\n%s", err, out)
	}
}
