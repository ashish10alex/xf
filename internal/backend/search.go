package backend

import (
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
)

// Found is a project Search found: its root, as under the folder searched,
// and its tool.
type Found struct {
	Root    string
	Backend string
}

// skipDirs are folders that hold no projects of the user's own: installed
// packages and build output.
var skipDirs = map[string]bool{"node_modules": true, "dbt_packages": true, "target": true}

// Search lists the projects in the folder dir and the folders below it, in
// the order a walk finds them. want, when not empty, is the tool to look
// for; otherwise a folder that is the root of both is the first of Names'.
// dir may be a symlink; the folders below it are walked without following
// symlinks. Hidden folders, installed packages, build output and the
// folders inside a project are skipped, and so are folders that cannot be
// read.
func Search(dir, want string) ([]Found, error) {
	if want != "" {
		if _, ok := markers[want]; !ok {
			return nil, fmt.Errorf("unknown backend %q: want %s", want, strings.Join(Names, " or "))
		}
	}
	real, err := filepath.EvalSymlinks(dir)
	if err != nil {
		return nil, err
	}
	if info, err := os.Stat(real); err != nil {
		return nil, err
	} else if !info.IsDir() {
		return nil, fmt.Errorf("%s is not a folder", dir)
	}
	var out []Found
	err = filepath.WalkDir(real, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			if path == real {
				return err
			}
			return nil // unreadable: skipped
		}
		if !d.IsDir() {
			return nil
		}
		if path != real && (strings.HasPrefix(d.Name(), ".") || skipDirs[d.Name()]) {
			return filepath.SkipDir
		}
		name := toolOf(path, want)
		if name == "" {
			return nil
		}
		// Found under the folder as it was given, not where its symlink
		// points.
		root := dir
		if rel, err := filepath.Rel(real, path); err == nil && rel != "." {
			root = filepath.Join(dir, rel)
		}
		out = append(out, Found{Root: root, Backend: name})
		return filepath.SkipDir
	})
	return out, err
}

// toolOf is the tool whose project dir is the root of: want's, or the
// first of Names' when want is empty; "" for none.
func toolOf(dir, want string) string {
	for _, name := range Names {
		if (want == "" || want == name) && hasMarker(dir, name) {
			return name
		}
	}
	return ""
}
