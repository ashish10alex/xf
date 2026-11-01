package dataform

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"time"

	"github.com/ashish10alex/xf/internal/backend/cmdrun"
	"github.com/ashish10alex/xf/internal/project"
)

// Backend compiles Dataform projects with the dataform CLI at Bin, or
// when Bin is "", with the one on PATH each time it is needed: xf web
// opens a project before the CLI is installed, and compiles fail until it
// is.
type Backend struct {
	Bin string
	// Names is what its compiles and runs add to the names the project
	// builds.
	Names Names
}

// Name is "dataform".
func (Backend) Name() string { return "dataform" }

// Variant names the binary and the Names, as 8 hex characters of a hash
// of the binary's path with symlinks resolved and of any Names, so clients
// using different Dataform CLIs, or compiling to other names, never share
// an engine, and an upgrade that repoints a symlink starts a fresh one.
func (b Backend) Variant() string {
	key := "bin=" + project.RealPath(b.Bin)
	if !b.Names.IsZero() {
		// Only then, so a project with none keeps its engine's name.
		key += "\x00prefix=" + b.Names.TablePrefix + "\x00schema=" + b.Names.SchemaSuffix + "\x00database=" + b.Names.DatabaseSuffix
	}
	sum := sha256.Sum256([]byte(key))
	return hex.EncodeToString(sum[:])[:8]
}

// Compile runs `dataform compile --json` in root with b's Names (see
// CompileWith).
func (b Backend) Compile(ctx context.Context, root string) (*project.Graph, error) {
	return CompileWith(ctx, b.Bin, root, b.Names.Args())
}

// Version asks the CLI for its version, e.g. "3.0.8".
func (b Backend) Version(ctx context.Context) (string, error) {
	prog, prefix, err := command(b.Bin)
	if err != nil {
		return "", err
	}
	out, err := cmdrun.Command(ctx, prog, append(prefix, "--version")...).CombinedOutput()
	if err != nil {
		return "", cmdrun.VersionError(out, err)
	}
	return cmdrun.FirstLine(stripANSI(string(out))), nil
}

// Parse parses a compile result (see ParseCompileJSON).
func (Backend) Parse(data []byte) (*project.Graph, error) { return ParseCompileJSON(data) }

// Watch watches definitions/, includes/ and workflow_settings.yaml (see Watch).
func (Backend) Watch(ctx context.Context, root string, debounce time.Duration) (<-chan struct{}, error) {
	return Watch(ctx, root, debounce)
}
