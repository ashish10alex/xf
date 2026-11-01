package cmdrun

import (
	"bytes"
	"context"
	"errors"
	"os/exec"
	"runtime"
	"sync"
	"time"
)

// waitDelay bounds how long Wait waits for a cancelled call's output once
// its process tree is stopped.
const waitDelay = 2 * time.Second

// Cmd is a short call to a tool (a compile, a parse, git): an exec.Cmd
// that runs in a process tree of its own, opening no window on Windows.
// Cancelling its context stops the whole tree, so a tool's workers can't
// outlive it, and so do Wait and the methods that wait.
type Cmd struct {
	*exec.Cmd
	ctx  context.Context
	mu   sync.Mutex
	tree *processTree
	// leave lets what the command left running go on once it exits by
	// itself: git's file-system monitor daemon (see Git).
	leave bool
}

// Command is exec.CommandContext for a Cmd.
func Command(ctx context.Context, name string, args ...string) *Cmd {
	c := &Cmd{Cmd: exec.CommandContext(ctx, name, args...), ctx: ctx}
	isolate(c.Cmd)
	c.Cmd.Cancel = c.kill
	c.Cmd.WaitDelay = waitDelay
	return c
}

func (c *Cmd) kill() error {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.tree != nil {
		c.tree.kill()
		return nil
	}
	return c.Process.Kill()
}

// Start starts the command in its process tree.
func (c *Cmd) Start() error {
	if err := c.Cmd.Start(); err != nil {
		return err
	}
	tree, err := attach(c.Process)
	if err != nil {
		_ = c.Process.Kill()
		_ = c.Cmd.Wait()
		return err
	}
	c.mu.Lock()
	c.tree = tree
	c.mu.Unlock()
	return nil
}

// Wait waits for the command to exit, then stops what's left of its tree,
// unless the command leaves it and wasn't cancelled.
func (c *Cmd) Wait() error {
	err := c.Cmd.Wait()
	c.mu.Lock()
	if c.tree != nil {
		if c.leave && c.ctx.Err() == nil {
			c.tree.release()
		} else {
			c.tree.kill()
		}
	}
	c.mu.Unlock()
	return err
}

// Run starts the command and waits for it.
func (c *Cmd) Run() error {
	if err := c.Start(); err != nil {
		return err
	}
	return c.Wait()
}

// Output runs the command and returns its standard output. As with
// exec.Cmd, an *exec.ExitError carries what it wrote to stderr.
func (c *Cmd) Output() ([]byte, error) {
	if c.Stdout != nil {
		return nil, errors.New("cmdrun: Stdout already set")
	}
	var out, stderr bytes.Buffer
	c.Stdout = &out
	captured := c.Stderr == nil
	if captured {
		c.Stderr = &stderr
	}
	err := c.Run()
	var exitErr *exec.ExitError
	if captured && errors.As(err, &exitErr) {
		exitErr.Stderr = stderr.Bytes()
	}
	return out.Bytes(), err
}

// CombinedOutput runs the command and returns its standard output and
// standard error.
func (c *Cmd) CombinedOutput() ([]byte, error) {
	if c.Stdout != nil || c.Stderr != nil {
		return nil, errors.New("cmdrun: Stdout or Stderr already set")
	}
	var out bytes.Buffer
	c.Stdout, c.Stderr = &out, &out
	err := c.Run()
	return out.Bytes(), err
}

// Git is a Command running git in dir. On Windows it allows long paths
// (core.longpaths), without which git can't read a repository whose root
// is deeper than about 200 characters.
//
// What git leaves running when it exits by itself goes on: with
// core.fsmonitor set, git starts the repository's file-system monitor
// daemon and waits about a second for it, to use it from then on. Killed
// with git, it was started again by the next call, so every one took that
// second. A cancelled call still stops its whole tree.
func Git(ctx context.Context, dir string, args ...string) *Cmd {
	pre := []string{"-C", dir}
	if runtime.GOOS == "windows" {
		pre = append(pre, "-c", "core.longpaths=true")
	}
	c := Command(ctx, "git", append(pre, args...)...)
	c.leave = true
	return c
}
