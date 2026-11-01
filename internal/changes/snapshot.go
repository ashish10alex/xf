package changes

import (
	"archive/tar"
	"context"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"strings"

	"github.com/ashish10alex/xf/internal/backend/cmdrun"
)

// extractSnapshot writes the project in dir as of commit sha into dest.
// Run from the project folder, git archive includes only that folder, with
// paths relative to it, so dest holds the project as the root does.
func extractSnapshot(ctx context.Context, dir, sha, dest string) error {
	cmd := cmdrun.Git(ctx, dir, "archive", "--format=tar", sha)
	var stderr strings.Builder
	cmd.Stderr = &stderr
	out, err := cmd.StdoutPipe()
	if err != nil {
		return err
	}
	if err := cmd.Start(); err != nil {
		return fmt.Errorf("git archive: %w", err)
	}
	extractErr := untar(out, dest)
	// Let git finish writing: it pads the archive past the end-of-archive
	// marker untar stops at, and blocks on a full pipe (Windows pipes are
	// small) until it's read.
	_, _ = io.Copy(io.Discard, out)
	waitErr := cmd.Wait()
	switch {
	case waitErr != nil:
		return fmt.Errorf("could not extract %s: git archive: %s", short(sha), strings.TrimSpace(firstNonEmpty(stderr.String(), waitErr.Error())))
	case extractErr != nil:
		return fmt.Errorf("could not extract %s: %w", short(sha), extractErr)
	}
	return nil
}

func firstNonEmpty(s ...string) string {
	for _, x := range s {
		if strings.TrimSpace(x) != "" {
			return x
		}
	}
	return ""
}

// untar extracts a tar stream into dest, refusing entries that would land
// outside it.
func untar(r io.Reader, dest string) error {
	tr := tar.NewReader(r)
	for {
		h, err := tr.Next()
		if errors.Is(err, io.EOF) {
			return nil
		}
		if err != nil {
			return err
		}
		name := filepath.Clean(filepath.FromSlash(h.Name))
		if name == "." || filepath.IsAbs(name) || name == ".." || strings.HasPrefix(name, ".."+string(filepath.Separator)) {
			continue
		}
		path := filepath.Join(dest, name)
		switch h.Typeflag {
		case tar.TypeDir:
			if err := os.MkdirAll(path, 0o755); err != nil {
				return err
			}
		case tar.TypeReg:
			if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
				return err
			}
			f, err := os.OpenFile(path, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, fs.FileMode(h.Mode)&0o777|0o600)
			if err != nil {
				return err
			}
			_, err = io.Copy(f, tr)
			if cerr := f.Close(); err == nil {
				err = cerr
			}
			if err != nil {
				return err
			}
		case tar.TypeSymlink:
			if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
				return err
			}
			if err := os.Symlink(h.Linkname, path); err != nil {
				return err
			}
		}
		// git archive writes a pax global header (the commit) and nothing
		// else of other types.
	}
}

// mirrorTree recreates src under dest with hard links, copying files where
// linking fails (another volume). A symlink to the real node_modules does
// not work: newer Dataform CLIs resolve the real path of every required
// module and reject anything outside the project directory. Links inside
// the tree (.bin, pnpm) are recreated by mirrorLink.
func mirrorTree(src, dest string) error {
	return filepath.WalkDir(src, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel, err := filepath.Rel(src, path)
		if err != nil {
			return err
		}
		to := filepath.Join(dest, rel)
		if handled, err := mirrorLink(path, to, d); handled {
			return err
		}
		switch {
		case d.IsDir():
			return os.MkdirAll(to, 0o755)
		case d.Type().IsRegular():
			if err := os.Link(path, to); err == nil {
				return nil
			}
			return copyFile(path, to)
		}
		return nil
	})
}

func copyFile(src, dest string) error {
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer func() { _ = in.Close() }()
	info, err := in.Stat()
	if err != nil {
		return err
	}
	out, err := os.OpenFile(dest, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, info.Mode().Perm())
	if err != nil {
		return err
	}
	_, err = io.Copy(out, in)
	if cerr := out.Close(); err == nil {
		err = cerr
	}
	return err
}

// snapshot extracts the project in dir as of sha into a new directory under
// parent, with the root's node_modules mirrored in when the snapshot has a
// package.json (only such projects load @dataform/core from node_modules;
// the others use the core the CLI installs), and each of mirror that dir
// has. The caller removes it.
func snapshot(ctx context.Context, dir, sha, parent string, mirror []string) (string, error) {
	if err := os.MkdirAll(parent, 0o755); err != nil {
		return "", err
	}
	dest, err := os.MkdirTemp(parent, short(sha)+"-")
	if err != nil {
		return "", err
	}
	if err := extractSnapshot(ctx, dir, sha, dest); err != nil {
		_ = os.RemoveAll(dest)
		return "", err
	}
	modules := filepath.Join(dir, "node_modules")
	if _, err := os.Stat(filepath.Join(dest, "package.json")); err == nil {
		if info, err := os.Stat(modules); err == nil && info.IsDir() {
			if err := mirrorTree(modules, filepath.Join(dest, "node_modules")); err != nil {
				_ = os.RemoveAll(dest)
				return "", fmt.Errorf("could not mirror node_modules into the snapshot of %s: %w", short(sha), err)
			}
		}
	}
	for _, m := range mirror {
		src := filepath.Join(dir, m)
		if info, err := os.Stat(src); err != nil || !info.IsDir() {
			continue
		}
		// A commit that tracks the directory keeps its own.
		if _, err := os.Lstat(filepath.Join(dest, m)); err == nil {
			continue
		}
		if err := mirrorTree(src, filepath.Join(dest, m)); err != nil {
			_ = os.RemoveAll(dest)
			return "", fmt.Errorf("could not mirror %s into the snapshot of %s: %w", m, short(sha), err)
		}
	}
	return dest, nil
}

// short is the first 7 characters of a commit SHA.
func short(sha string) string {
	if len(sha) > 7 {
		return sha[:7]
	}
	return sha
}
