package main

import (
	"os"
	"path/filepath"
	"runtime"
	"testing"
)

func TestPiped(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("Windows terminals may give programs pipes")
	}
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = r.Close(); _ = w.Close() }()
	f, err := os.Create(filepath.Join(t.TempDir(), "in.json"))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = f.Close() }()
	null, err := os.Open(os.DevNull)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = null.Close() }()

	if !piped(r) || !piped(f) {
		t.Error("a pipe or a file isn't piped")
	}
	if piped(null) {
		t.Error("/dev/null is piped") // a device, as a terminal is
	}
}
