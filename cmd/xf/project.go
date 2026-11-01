package main

import (
	"cmp"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/ashish10alex/xf/internal/backend"
	"github.com/ashish10alex/xf/internal/backend/dataform"
	"github.com/ashish10alex/xf/internal/backend/dbt"
	"github.com/ashish10alex/xf/internal/config"
	"github.com/ashish10alex/xf/internal/project"
)

// projectFlags say where the project is: --dir (the working directory by
// default) must be its root, and --backend picks the tool for a directory
// that is the root of both a Dataform and a dbt project.
type projectFlags struct {
	dir, backend *string
}

func addProjectFlags(fs *flag.FlagSet) projectFlags {
	return projectFlags{
		dir:     fs.String("dir", "", "the project's root `dir` (default: the working directory)"),
		backend: fs.String("backend", "", "the project's `tool`, "+strings.Join(backend.Names, " or ")+", for a directory that is both (default: detected)"),
	}
}

// find returns the project's root and the name of its tool.
func (p projectFlags) find() (root, name string, err error) {
	dir := *p.dir
	if dir == "" {
		if dir, err = os.Getwd(); err != nil {
			return "", "", err
		}
	}
	if root, err = filepath.Abs(dir); err != nil {
		return "", "", err
	}
	name, err = backend.Detect(root, *p.backend)
	return root, name, err
}

// given are the project flags as arguments, root and tool resolved, for
// another xf to open the same project.
func projectArgs(root, name string) []string {
	return []string{"--dir", root, "--backend", name}
}

// openBackend makes the backend for the named tool, finding its binary
// from settings.
func openBackend(name, root string, settings config.Loaded) (backend.Backend, error) {
	return openBackendFor(name, root, settings, false)
}

// openWebBackend is openBackend for xf web and the engine it starts: a
// Dataform project whose CLI isn't on PATH opens all the same, with a
// Backend that looks for it at each use, so the page shows the files and
// how to install it, and compiles once it is installed.
func openWebBackend(name, root string, settings config.Loaded) (backend.Backend, error) {
	return openBackendFor(name, root, settings, true)
}

func openBackendFor(name, root string, settings config.Loaded, withoutCLI bool) (backend.Backend, error) {
	if err := project.CheckRoot(root); err != nil {
		return nil, err
	}
	switch name {
	case backend.Dataform:
		bin, err := resolveBin(settings)
		if withoutCLI && errors.Is(err, dataform.ErrNotOnPath) {
			return dataform.Backend{Names: dataformNames(settings)}, nil
		}
		if err != nil {
			return nil, err
		}
		return dataform.Backend{Bin: bin, Names: dataformNames(settings)}, nil
	case backend.DBT:
		bin, err := resolveDbtBin(settings, root)
		if err != nil {
			return nil, err
		}
		b := dbt.New(bin, settings.DbtTarget, dbtArtifactDir, settings.DbtCompileWithHooks == "true")
		b.DefaultBranch = cmp.Or(settings.DefaultBranch, config.DefaultBranch)
		return b, nil
	}
	return nil, fmt.Errorf("unknown backend %q", name)
}

// dataformNames are what settings have a Dataform project's compiles
// and runs add to the names it builds.
func dataformNames(settings config.Loaded) dataform.Names {
	return dataform.Names{TablePrefix: settings.TablePrefix, SchemaSuffix: settings.SchemaSuffix, DatabaseSuffix: settings.DatabaseSuffix}
}

// dbtArtifactDir is where dbt writes its target and logs for the project at
// root: under the user cache directory, keyed by the project's real path,
// never in the project. The backend keeps each Variant in a directory of
// its own under it.
func dbtArtifactDir(root string) string {
	dir, err := os.UserCacheDir()
	if err != nil {
		dir = os.TempDir()
	}
	sum := sha256.Sum256([]byte(project.RealPath(root)))
	return filepath.Join(dir, "xf", "dbt", hex.EncodeToString(sum[:])[:16])
}

// requireDataform fails for a project that isn't Dataform's, for the
// commands only Dataform has.
func requireDataform(command, root, name string) error {
	if name != backend.Dataform {
		return fmt.Errorf("%s works on Dataform projects; %s is a %s project", command, root, name)
	}
	return nil
}
