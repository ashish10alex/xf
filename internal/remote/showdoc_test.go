package remote

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/ashish10alex/xf/internal/engine"
)

// shown records the files an editor's client is asked to open.
type shown chan string

func (s shown) open(_ context.Context, file string, line int) error {
	s <- fmt.Sprintf("%s:%d", filepath.Base(file), line)
	return nil
}

func (s shown) next(t *testing.T) string {
	t.Helper()
	select {
	case f := <-s:
		return f
	case <-time.After(5 * time.Second):
		t.Fatal("the editor wasn't asked")
		return ""
	}
}

// showDocProject starts a server for a project with two files, and returns
// its socket and the files.
func showDocProject(t *testing.T) (path, a, b string) {
	t.Helper()
	root, a, b := twoFiles(t)
	path = socketPath(t)
	startServer(t, path, root, engine.Config{Compile: fixed(loadGraph(t, "basic.json"))}, time.Minute)
	return path, a, b
}

// twoFiles makes a project with two files, and returns its root and them.
func twoFiles(t *testing.T) (root, a, b string) {
	t.Helper()
	root = t.TempDir()
	a, b = filepath.Join(root, "definitions", "a.sqlx"), filepath.Join(root, "definitions", "b.sqlx")
	for _, f := range []string{a, b} {
		if err := os.MkdirAll(filepath.Dir(f), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(f, []byte("select 1\n"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	return root, a, b
}

// reportActive has c report file as active and waits until tui hears of
// it, so the server has taken it.
func reportActive(t *testing.T, c, tui *Client, file string) {
	t.Helper()
	c.ActiveFile("BufEnter", file)
	timeout := time.After(5 * time.Second)
	for {
		select {
		case ev := <-tui.Editor():
			if ev.File == "definitions/"+filepath.Base(file) {
				return
			}
		case <-timeout:
			t.Fatal("the active file didn't reach the TUI")
		}
	}
}

// xf/showDocument goes to the editor that reported the active file last.
func TestShowDocumentGoesToTheLastActiveEditor(t *testing.T) {
	path, a, b := showDocProject(t)
	tui := connect(t, path, noStart)
	first, second := connect(t, path, noStart), connect(t, path, noStart)
	inFirst, inSecond := make(shown, 4), make(shown, 4)
	first.HandleShowDocument(inFirst.open)
	second.HandleShowDocument(inSecond.open)

	reportActive(t, first, tui, a)
	reportActive(t, second, tui, b)
	if err := tui.ShowDocument(t.Context(), a, 7); err != nil {
		t.Fatal(err)
	}
	if got := inSecond.next(t); got != "a.sqlx:7" {
		t.Errorf("the second editor opened %s", got)
	}
	reportActive(t, first, tui, b)
	if err := tui.ShowDocument(t.Context(), b, 0); err != nil {
		t.Fatal(err)
	}
	if got := inFirst.next(t); got != "b.sqlx:0" {
		t.Errorf("the first editor opened %s", got)
	}
	if len(inSecond) > 0 {
		t.Errorf("the second editor was asked again: %s", <-inSecond)
	}
}

func TestShowDocumentWithoutAnEditor(t *testing.T) {
	path, a, _ := showDocProject(t)
	tui := connect(t, path, noStart)
	if err := tui.ShowDocument(t.Context(), a, 0); !errors.Is(err, ErrNoEditor) {
		t.Errorf("with no editor: %v, want ErrNoEditor", err)
	}

	// An editor whose client can't open files.
	plain := connect(t, path, noStart)
	reportActive(t, plain, tui, a)
	if err := tui.ShowDocument(t.Context(), a, 0); !errors.Is(err, ErrNoEditor) {
		t.Errorf("with an editor that can't open files: %v, want ErrNoEditor", err)
	}

	// An editor that goes.
	gone, err := Connect(context.Background(), ClientConfig{Dial: Dialer(path, noStart), Parse: plain.parse})
	if err != nil {
		t.Fatal(err)
	}
	gone.HandleShowDocument(make(shown, 1).open)
	reportActive(t, gone, tui, a)
	gone.Close()
	if err := tui.ShowDocument(t.Context(), a, 0); !errors.Is(err, ErrNoEditor) {
		t.Errorf("after the editor went: %v, want ErrNoEditor", err)
	}
}

// An editor that fails to open the file says why.
func TestShowDocumentFails(t *testing.T) {
	path, a, _ := showDocProject(t)
	tui, ed := connect(t, path, noStart), connect(t, path, noStart)
	ed.HandleShowDocument(func(context.Context, string, int) error { return errors.New("no such window") })
	reportActive(t, ed, tui, a)
	if err := tui.ShowDocument(t.Context(), a, 0); err == nil || errors.Is(err, ErrNoEditor) || err.Error() != "no such window" {
		t.Errorf("got %v", err)
	}
}

// An editor's client tells a new server the file it reported last, so the
// server knows where to send xf/showDocument.
func TestActiveFileOutlivesTheServer(t *testing.T) {
	root, a, _ := twoFiles(t)
	path := socketPath(t)
	ecfg := engine.Config{Compile: fixed(loadGraph(t, "basic.json"))}
	first := startServer(t, path, root, ecfg, time.Minute)
	var once sync.Once
	start := func() error {
		once.Do(func() { startServer(t, path, root, ecfg, time.Minute) })
		return nil
	}
	ed := connect(t, path, start)
	inEd := make(shown, 1)
	ed.HandleShowDocument(inEd.open)
	tui := connect(t, path, start)
	reportActive(t, ed, tui, a)

	first.stop()
	<-first.done
	// Both move to the new server; the editor tells it its file again.
	deadline := time.Now().Add(10 * time.Second)
	for {
		err := tui.ShowDocument(t.Context(), a, 3)
		if err == nil {
			break
		}
		if !errors.Is(err, ErrNoEditor) || time.Now().After(deadline) {
			t.Fatalf("after the server changed: %v", err)
		}
		time.Sleep(20 * time.Millisecond)
	}
	if got := inEd.next(t); got != "a.sqlx:3" {
		t.Errorf("the editor opened %s", got)
	}
}

// A client that connects after an editor has reported its file is told it
// at once, while it says hello: neither end may wait for the other to read.
func TestClientJoinsAfterAnActiveFile(t *testing.T) {
	path, a, _ := showDocProject(t)
	ed, tui := connect(t, path, noStart), connect(t, path, noStart)
	reportActive(t, ed, tui, a)
	late := connect(t, path, noStart)
	select {
	case ev := <-late.Editor():
		if ev.File != "definitions/a.sqlx" {
			t.Errorf("got %+v", ev)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("the late client wasn't told the active file")
	}
	if err := late.ShowDocument(t.Context(), a, 0); !errors.Is(err, ErrNoEditor) {
		t.Errorf("show document from the late client: %v, want ErrNoEditor", err)
	}
}
