package main

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/ashish10alex/xf/internal/engine"
	"github.com/ashish10alex/xf/internal/remote"
)

// explainCompile is err from a check of client's project, with the
// engine's ErrStale explained by compileError.
func explainCompile(ctx context.Context, client *remote.Client, err error) error {
	if err == nil || err.Error() != engine.ErrStale.Error() {
		return err
	}
	return compileError(err, compiledState(ctx, client))
}

// compiledState is client's state once it shows a finished compile. The
// engine replies to a check before the state with that compile reaches the
// client, so the client's state can still say it is compiling; after a
// few seconds it is whatever the client has.
func compiledState(ctx context.Context, client *remote.Client) engine.State {
	states, unsub := client.Subscribe()
	defer unsub()
	timeout := time.After(5 * time.Second)
	st := client.State()
	for {
		if st.Compiles > 0 && !st.Compiling && !st.Partial {
			return st
		}
		select {
		case st = <-states:
		case <-timeout:
			return st
		case <-ctx.Done():
			return st
		}
	}
}

// compileError explains the engine's ErrStale on the command line, where
// its hint (the TUI's e key) means nothing: it gives the compile's own
// error, on one line, or its first compile error and how many there are,
// from st, the engine's state once the check gave up. Any other error is
// returned as is.
func compileError(err error, st engine.State) error {
	if err == nil || err.Error() != engine.ErrStale.Error() {
		return err
	}
	switch {
	case st.Err != nil:
		// The first lines say what is wrong (the command, then the
		// compiler's own message); a stack trace follows them.
		lines := strings.Split(strings.TrimSpace(st.Err.Error()), "\n")
		return fmt.Errorf("the project doesn't compile: %s", strings.Join(lines[:min(len(lines), 2)], " — "))
	case len(st.Errors) > 0:
		e := st.Errors[0]
		where := e.FileName
		if where != "" {
			where += ": "
		}
		n := ""
		if len(st.Errors) > 1 {
			n = fmt.Sprintf(" (%d)", len(st.Errors))
		}
		return fmt.Errorf("the project has compile errors%s: %s%s", n, where, e.Message)
	}
	return errors.New("the project has compile errors")
}
