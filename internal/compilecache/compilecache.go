// Package compilecache keeps a project's last good compile on disk, so the
// next xf serve shows it at once instead of waiting for `dataform compile`
// or `dbt compile`.
// Each saved compile carries a Fingerprint of what it was compiled from; a
// saved compile whose fingerprint still matches the project is as good as a
// new one, and one that doesn't is shown as outdated while the project
// compiles again.
package compilecache

import (
	"bufio"
	"bytes"
	"cmp"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/ashish10alex/xf/internal/backend/dataform"
	"github.com/ashish10alex/xf/internal/backend/dbt"
	"github.com/ashish10alex/xf/internal/project"
)

// formatVersion is part of every saved file; a different one is ignored.
const formatVersion = 1

// sourceDirs are the project folders the CLI compiles from.
var sourceDirs = []string{"definitions", "includes"}

// rootFiles are the other files a compile depends on, relative to the
// project root. Missing ones count as a state of their own.
var rootFiles = []string{
	dataform.SettingsFile,
	"dataform.json",
	"package.json",
	"package-lock.json",
	"yarn.lock",
	"pnpm-lock.yaml",
	filepath.Join("node_modules", "@dataform", "core", "package.json"),
}

// Fingerprint is what a compile was made from.
type Fingerprint struct {
	// Files hashes the path, size and modification time of every file the
	// compile reads.
	Files string `json:"files"`
	// CLI is the dataform binary, followed to its real file, with that
	// file's and its package.json's size and modification time.
	CLI string `json:"cli"`
	// Tool is the tool the CLI is of, when it isn't Dataform: "dbt".
	Tool string `json:"tool,omitempty"`
	// Day is the UTC day. Projects often compute values from the date in
	// includes/, so a compile from an earlier day may differ from today's.
	Day string `json:"day"`
}

// Take fingerprints the project in root as bin would compile it at now.
// Take it before compiling: a file saved during the compile then makes the
// saved compile outdated, rather than wrongly current.
func Take(root, bin string, now time.Time) (Fingerprint, error) {
	h := sha256.New()
	stamp := func(rel string, info fs.FileInfo) {
		_, _ = fmt.Fprintf(h, "%s\x00%d\x00%d\n", filepath.ToSlash(rel), info.Size(), info.ModTime().UnixNano())
	}
	for _, rel := range rootFiles {
		info, err := os.Stat(filepath.Join(root, rel))
		switch {
		case err == nil:
			stamp(rel, info)
		case errors.Is(err, fs.ErrNotExist):
			_, _ = fmt.Fprintf(h, "%s\x00-\n", filepath.ToSlash(rel))
		default:
			return Fingerprint{}, err
		}
	}
	for _, dir := range sourceDirs {
		err := filepath.WalkDir(filepath.Join(root, dir), func(path string, d fs.DirEntry, err error) error {
			if err != nil {
				if errors.Is(err, fs.ErrNotExist) {
					return nil
				}
				return err
			}
			// Dotfiles are editor swap files, .DS_Store and the like,
			// which the CLI doesn't read.
			if strings.HasPrefix(d.Name(), ".") {
				if d.IsDir() {
					return filepath.SkipDir
				}
				return nil
			}
			if d.IsDir() {
				return nil
			}
			info, err := d.Info()
			if err != nil {
				return err
			}
			rel, _ := filepath.Rel(root, path)
			stamp(rel, info)
			return nil
		})
		if err != nil {
			return Fingerprint{}, err
		}
	}
	return Fingerprint{
		Files: hex.EncodeToString(h.Sum(nil)),
		CLI:   cliStamp(bin),
		Day:   now.UTC().Format("2006-01-02"),
	}, nil
}

// TakeDbt fingerprints the dbt project in root as bin would compile it at
// now: the files dbt.SourceFiles lists, and the binary itself. What else
// picks the compile, the target and whether hooks run, is the backend's
// Variant, which keeps a Store of its own.
func TakeDbt(root, bin string, now time.Time) (Fingerprint, error) {
	h := sha256.New()
	err := dbt.SourceFiles(root, func(rel string, info fs.FileInfo) {
		_, _ = fmt.Fprintf(h, "%s\x00%d\x00%d\n", filepath.ToSlash(rel), info.Size(), info.ModTime().UnixNano())
	})
	if err != nil {
		return Fingerprint{}, err
	}
	real := project.RealPath(bin)
	cli := real
	if info, err := os.Stat(real); err == nil {
		cli = fmt.Sprintf("%s\t%d:%d", real, info.Size(), info.ModTime().UnixNano())
	}
	return Fingerprint{
		Files: hex.EncodeToString(h.Sum(nil)),
		CLI:   cli,
		Tool:  "dbt",
		Day:   now.UTC().Format("2006-01-02"),
	}, nil
}

// cliStamp identifies the dataform binary. npm installs it as a link into
// the @dataform/cli package (on Windows, as a shim beside it), so the
// package.json above the real file says which version it is; an upgrade
// rewrites both.
func cliStamp(bin string) string {
	if bundle := dataform.NpmBundle(bin); bundle != "" {
		bin = bundle
	}
	real := project.RealPath(bin)
	parts := []string{real}
	if info, err := os.Stat(real); err == nil {
		parts = append(parts, fmt.Sprintf("%d:%d", info.Size(), info.ModTime().UnixNano()))
	}
	dir := filepath.Dir(real)
	for range 3 {
		if info, err := os.Stat(filepath.Join(dir, "package.json")); err == nil {
			parts = append(parts, fmt.Sprintf("package.json %d:%d", info.Size(), info.ModTime().UnixNano()))
			break
		}
		dir = filepath.Dir(dir)
	}
	return strings.Join(parts, "\t")
}

// Outdated says why a compile made from saved may differ from one made
// from now, or "" when it wouldn't.
func Outdated(saved, now Fingerprint) string {
	switch {
	case saved.CLI != now.CLI:
		return "the " + cmp.Or(now.Tool, "dataform") + " CLI changed"
	case saved.Files != now.Files:
		return "project files changed"
	case saved.Day != now.Day:
		return "compiled on an earlier day"
	}
	return ""
}

// Entry is a saved compile.
type Entry struct {
	Fingerprint Fingerprint   `json:"fingerprint"`
	CompiledAt  time.Time     `json:"compiledAt"`
	Took        time.Duration `json:"took"`
	// JSON is the compile's output; it isn't part of the header.
	JSON []byte `json:"-"`
}

// header is a saved file's first line; the compile JSON follows it.
type header struct {
	Version int    `json:"version"`
	Root    string `json:"root"`
	Entry
}

// Store keeps one saved compile per project in Dir.
type Store struct {
	Dir string
	// mu keeps saves one at a time, so an older compile never lands after
	// a newer one.
	mu sync.Mutex
}

func (s *Store) path(root string) string {
	sum := sha256.Sum256([]byte(project.RealPath(root)))
	return filepath.Join(s.Dir, hex.EncodeToString(sum[:])[:16]+".json")
}

// Load returns the compile saved for the project in root, or nil when
// there is none (or it can't be read).
func (s *Store) Load(root string) (*Entry, error) {
	data, err := os.ReadFile(s.path(root))
	if errors.Is(err, fs.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	line, rest, ok := bytes.Cut(data, []byte("\n"))
	if !ok {
		return nil, errors.New("saved compile has no header")
	}
	var h header
	if err := json.Unmarshal(line, &h); err != nil {
		return nil, fmt.Errorf("saved compile header: %w", err)
	}
	if h.Version != formatVersion || h.Root != project.RealPath(root) {
		return nil, nil
	}
	e := h.Entry
	e.JSON = rest
	return &e, nil
}

// Save keeps e as the project's saved compile, replacing the last one.
func (s *Store) Save(root string, e Entry) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := os.MkdirAll(s.Dir, 0o755); err != nil {
		return err
	}
	line, err := json.Marshal(header{Version: formatVersion, Root: project.RealPath(root), Entry: e})
	if err != nil {
		return err
	}
	tmp, err := os.CreateTemp(s.Dir, "tmp-*")
	if err != nil {
		return err
	}
	w := bufio.NewWriter(tmp)
	_, _ = w.Write(line)
	_ = w.WriteByte('\n')
	_, _ = w.Write(e.JSON)
	err = w.Flush()
	if cerr := tmp.Close(); err == nil {
		err = cerr
	}
	if err == nil {
		err = os.Rename(tmp.Name(), s.path(root))
	}
	if err != nil {
		_ = os.Remove(tmp.Name())
	}
	return err
}
