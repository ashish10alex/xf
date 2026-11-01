// Package dbt compiles dbt projects through the dbt CLI, dbt-core 1.x or dbt
// v2, and builds a project.Graph from the manifest it writes. It has no UI
// dependencies.
package dbt

import (
	"bytes"
	"cmp"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path"
	"path/filepath"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/ashish10alex/xf/internal/backend/cmdrun"
	"github.com/ashish10alex/xf/internal/backend/watch"
	"github.com/ashish10alex/xf/internal/project"
)

// ProjectFile marks the root of a dbt project.
const ProjectFile = "dbt_project.yml"

// BinEnv is the environment variable that points at the dbt binary.
const BinEnv = "DBT_BIN"

// Backend compiles dbt projects with the dbt CLI at Bin. Make one with New.
type Backend struct {
	// Bin is the dbt binary.
	Bin string
	// Target is the profile target dbt compiles with (--target); "" lets
	// dbt choose: $DBT_TARGET, else the profile's default.
	Target string
	// ArtifactDir is where dbt writes its target and logs for the project
	// at root, so xf never touches the project's own target/ or logs/,
	// which the user's runs and --state comparisons rely on. Each Variant
	// gets its own directory under it.
	ArtifactDir func(root string) string
	// CompileWithHooks compiles dbt v2 projects that have on-run-start or
	// on-run-end hooks. dbt v2 runs those hooks when it compiles, so by
	// default xf only parses such projects.
	CompileWithHooks bool
	// DefaultBranch is the branch Suggest compares the working tree with;
	// "" compares it with HEAD.
	DefaultBranch string

	major   *versionOnce
	hooks   *hookCache
	changed *sync.Mutex // held while dbt ls lists Changed Actions
	// deferred is what the project's compiles defer to (defer.go).
	deferred *deferState
}

// hookCache remembers, per project root, that a parse found no hooks, and
// the fingerprint of the files that could add one, so later compiles skip
// the parse while those files are unchanged.
type hookCache struct {
	mu   sync.Mutex
	none map[string]string // root → fingerprint when it had no hooks
}

type versionOnce struct {
	once    sync.Once
	major   int
	version string
}

// New makes a backend for the dbt binary bin, compiling with target ("" for
// dbt's default).
func New(bin, target string, artifactDir func(root string) string, compileWithHooks bool) *Backend {
	return &Backend{Bin: bin, Target: target, ArtifactDir: artifactDir, CompileWithHooks: compileWithHooks, major: &versionOnce{}, hooks: &hookCache{none: map[string]string{}}, changed: &sync.Mutex{}, deferred: &deferState{}}
}

// Name is "dbt".
func (*Backend) Name() string { return "dbt" }

// Variant names the settings that change what a compile produces, the
// binary, the target and CompileWithHooks, as 8 hex characters: backends
// that differ in them keep separate artifacts and engines. The binary is
// its path with symlinks resolved, so dbt-core and dbt v2 never share an
// engine or partial-parse state, and a Homebrew upgrade, which repoints
// the dbt symlink, starts a fresh one.
func (b *Backend) Variant() string {
	sum := sha256.Sum256(fmt.Appendf(nil, "bin=%s\ntarget=%s\nhooks=%t", project.RealPath(b.Bin), b.Target, b.CompileWithHooks))
	return hex.EncodeToString(sum[:])[:8]
}

// Parse parses the trimmed manifest a graph's JSON holds.
func (*Backend) Parse(data []byte) (*project.Graph, error) {
	m, err := parseManifest(data)
	if err != nil {
		return nil, err
	}
	return m.graph()
}

// watchSpec is what affects a dbt compile: every directory but dbt's own
// output, and the project's settings.
var watchSpec = watch.Spec{
	Skip: []string{"target", "logs"},
	Exts: map[string]bool{
		".sql": true, ".yml": true, ".yaml": true, ".csv": true,
		".py": true, ".md": true, ".jinja": true, ".jinja2": true,
	},
	RootFiles: []string{ProjectFile, "packages.yml", "dependencies.yml", "selectors.yml", "profiles.yml"},
}

// SourceFiles visits the files a compile of the project at root reads, as
// far as xf can tell: the ones Watch watches, in a fixed order, each with
// its path relative to root, and the profiles.yml outside the project
// ($DBT_PROFILES_DIR's, else ~/.dbt's) under its absolute path. A saved
// compile is as good as a new one while none of them has changed.
func SourceFiles(root string, visit func(rel string, info fs.FileInfo)) error {
	err := watchSpec.Walk(root, visit)
	if err != nil {
		return err
	}
	dir := os.Getenv("DBT_PROFILES_DIR")
	if dir == "" {
		if home, err := os.UserHomeDir(); err == nil {
			dir = filepath.Join(home, ".dbt")
		}
	}
	profiles := filepath.Join(dir, "profiles.yml")
	if info, err := os.Stat(profiles); err == nil && dir != "" {
		visit(profiles, info)
	}
	return nil
}

// Watch reports changes to the project's files (see watch.Watch).
func (*Backend) Watch(ctx context.Context, root string, debounce time.Duration) (<-chan struct{}, error) {
	return watch.Watch(ctx, root, watchSpec, debounce)
}

// Compile compiles the project at root. dbt-core compiles without running
// anything in the warehouse. dbt v2 runs the project's on-run-start and
// on-run-end hooks when it compiles, so for v2 xf parses first and, when
// there are hooks, keeps the parsed graph, whose SQL is uncompiled Jinja,
// and says so in Graph.Notice (unless CompileWithHooks). Once a parse has
// found no hooks, later compiles skip it until a dbt_project.yml changes.
//
// Errors dbt reports end up in Graph.Errors, with an empty graph when dbt
// wrote no manifest; an error is returned only when dbt reported nothing
// usable.
func (b *Backend) Compile(ctx context.Context, root string) (*project.Graph, error) {
	dir := b.variantDir(root)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return nil, err
	}
	major, version := b.version(ctx)
	if major != 1 && !b.CompileWithHooks && !b.knownHookless(root) {
		fp := hookFingerprint(root)
		m, err := b.run(ctx, root, dir, "parse")
		if err != nil {
			return nil, err
		}
		if len(m.hooks()) == 0 && len(m.Errors) == 0 {
			b.rememberHookless(root, fp)
		}
		if hooks := m.hooks(); len(hooks) > 0 || len(m.Errors) > 0 {
			if len(hooks) > 0 {
				m.Notice = hooksNotice(version, hooks)
			}
			return m.graph()
		}
	}
	m, err := b.run(ctx, root, dir, "compile")
	if err != nil {
		return nil, err
	}
	return m.graph()
}

func hooksNotice(version string, hooks []node) string {
	return fmt.Sprintf("SQL not compiled: %s runs on-run-start and on-run-end hooks when it compiles, and this project has %d (in %s). Set dbtCompileWithHooks to compile anyway.",
		cmp.Or(version, "dbt"), len(hooks), strings.Join(hookFiles(hooks), ", "))
}

// knownHookless reports whether a parse found no hooks in the project at
// root and nothing that could add one has changed since.
func (b *Backend) knownHookless(root string) bool {
	b.hooks.mu.Lock()
	fp, ok := b.hooks.none[root]
	b.hooks.mu.Unlock()
	return ok && fp == hookFingerprint(root)
}

func (b *Backend) rememberHookless(root, fp string) {
	b.hooks.mu.Lock()
	b.hooks.none[root] = fp
	b.hooks.mu.Unlock()
}

// hookFingerprint identifies the state of the files that can define hooks:
// on-run-start and on-run-end live only in dbt_project.yml, the project's
// and each installed package's.
func hookFingerprint(root string) string {
	files := []string{filepath.Join(root, ProjectFile)}
	for _, dir := range []string{"dbt_packages", "dbt_modules"} {
		matches, _ := filepath.Glob(filepath.Join(root, dir, "*", ProjectFile))
		files = append(files, matches...)
	}
	var b strings.Builder
	for _, f := range files {
		b.WriteString(f)
		if info, err := os.Stat(f); err == nil {
			fmt.Fprintf(&b, " %d %d", info.Size(), info.ModTime().UnixNano())
		}
		b.WriteByte('\n')
	}
	return b.String()
}

// version returns dbt's major version and how it names itself, once per
// backend. It is 0 when unknown, which is treated as v2: parsing first is
// the safe side.
func (b *Backend) version(ctx context.Context) (int, string) {
	b.major.once.Do(func() {
		out, err := cmdrun.Command(ctx, b.Bin, "--version").CombinedOutput()
		if err != nil {
			return
		}
		b.major.major, b.major.version = parseVersion(string(out))
	})
	return b.major.major, b.major.version
}

// Version asks dbt how it names itself, e.g. "dbt 2.0.6" or "dbt-core
// 1.10.4", afresh each time.
func (b *Backend) Version(ctx context.Context) (string, error) {
	out, err := cmdrun.Command(ctx, b.Bin, "--version").CombinedOutput()
	if err != nil {
		return "", cmdrun.VersionError(out, err)
	}
	if _, version := parseVersion(string(out)); version != "" {
		return version, nil
	}
	return cmdrun.FirstLine(stripANSI(string(out))), nil
}

var (
	// dbt v2 prints "dbt 2.0.6"; dbt-core prints "Core:\n  - installed: 1.10.4".
	v2Version   = regexp.MustCompile(`(?m)^dbt (\d+)\.(\d+)\S*`)
	coreVersion = regexp.MustCompile(`(?m)installed:\s*((\d+)\.\d+\S*)`)
)

func parseVersion(out string) (int, string) {
	out = stripANSI(out)
	if m := coreVersion.FindStringSubmatch(out); m != nil {
		n, _ := strconv.Atoi(m[2])
		return n, "dbt-core " + m[1]
	}
	if m := v2Version.FindStringSubmatch(out); m != nil {
		n, _ := strconv.Atoi(m[1])
		return n, m[0]
	}
	return 0, ""
}

// run runs a dbt command that writes a manifest into dir/target and reads
// it, with the errors dbt logged.
func (b *Backend) run(ctx context.Context, root, dir, command string) (*manifest, error) {
	inv, err := b.invoke(ctx, root, root, dir, command)
	if err != nil {
		return nil, err
	}
	errs := inv.errors()
	switch {
	case inv.manifest != nil:
		m, err := parseManifest(inv.manifest)
		if err != nil {
			return nil, err
		}
		if inv.failed != nil {
			m.Errors = errs
		}
		return m, nil
	case len(errs) > 0:
		return &manifest{Errors: errs}, nil
	case inv.failed != nil:
		return nil, fmt.Errorf("%s %s: %w", b.Bin, command, inv.failed)
	}
	return nil, fmt.Errorf("%s %s wrote no manifest", b.Bin, command)
}

// invocation is what a dbt command left behind.
type invocation struct {
	// manifest is the manifest it wrote; nil when it wrote none.
	manifest       []byte
	stdout, stderr []byte
	// failed is how it failed, its exit status; nil when it succeeded.
	failed error
}

// invoke runs a dbt command on the project in projectDir, writing its
// target and logs under dir, from cwd: where dbt-core looks for
// profiles.yml first. A project in another directory than cwd (a snapshot)
// gets cwd's profiles.yml, when there is one, so it is compiled with the
// same profile. An error means the command didn't run, or ctx is done.
func (b *Backend) invoke(ctx context.Context, cwd, projectDir, dir, command string, extra ...string) (*invocation, error) {
	target := filepath.Join(dir, "target")
	manifestPath := filepath.Join(target, "manifest.json")
	// A failed parse leaves the last manifest behind; never show it as this
	// compile's.
	if err := os.Remove(manifestPath); err != nil && !errors.Is(err, fs.ErrNotExist) {
		return nil, err
	}
	args := []string{command, "--project-dir", projectDir, "--target-path", target, "--log-path", filepath.Join(dir, "logs"), "--log-format", "json"}
	if projectDir != cwd {
		if _, err := os.Stat(filepath.Join(cwd, "profiles.yml")); err == nil {
			args = append(args, "--profiles-dir", cwd)
		}
	}
	args = append(args, extra...)
	if command == "compile" && projectDir == cwd {
		// Defer to prod: the project's own compiles, never a snapshot's.
		args = append(args, DeferArgs(b.DeferState())...)
	}
	if b.Target != "" {
		args = append(args, "--target", b.Target)
	}
	cmd := cmdrun.Command(ctx, b.Bin, args...)
	cmd.Dir = cwd
	var stdout, stderr bytes.Buffer
	cmd.Stdout, cmd.Stderr = &stdout, &stderr
	runErr := cmd.Run()
	if ctx.Err() != nil {
		return nil, ctx.Err()
	}
	inv := &invocation{stdout: stdout.Bytes(), stderr: stderr.Bytes(), failed: runErr}
	data, err := os.ReadFile(manifestPath)
	if err == nil {
		inv.manifest = data
	}
	return inv, nil
}

// errors are the errors dbt logged or, when it failed without logging one,
// what else it printed.
func (inv *invocation) errors() []project.CompilationError {
	errs := logErrors(inv.stdout)
	if inv.failed != nil && len(errs) == 0 {
		if detail := outputDetail(inv.stdout, inv.stderr); detail != "" {
			errs = []project.CompilationError{{Message: detail}}
		}
	}
	return errs
}

// hooks are the on-run-start and on-run-end hooks of the project and its
// packages: dbt lists them as operation nodes.
func (m *manifest) hooks() []node {
	var out []node
	for _, id := range sortedKeys(m.Nodes) {
		if n := m.Nodes[id]; n.ResourceType == "operation" {
			out = append(out, n)
		}
	}
	return out
}

func hookFiles(hooks []node) []string {
	var out []string
	for _, h := range hooks {
		f := filepath.FromSlash(path.Clean(project.SlashPath(h.OriginalFilePath)))
		if !slices.Contains(out, f) {
			out = append(out, f)
		}
	}
	return out
}
