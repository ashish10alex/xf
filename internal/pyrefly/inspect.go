package pyrefly

import (
	"bytes"
	"context"
	"encoding/json"
	"path/filepath"
	"strings"
	"time"

	"github.com/ashish10alex/xf/internal/backend/cmdrun"
)

// inspectTimeout bounds `pyrefly dump-config` and `pyrefly --version`.
const inspectTimeout = 3 * time.Second

// EnvInfo is what `pyrefly dump-config` reports for a file or project:
// the config file pyrefly loaded ("" for its default configuration), the
// Python interpreter it resolved, and the site-packages paths it searches
// for imports.
type EnvInfo struct {
	Config       string   `json:"config,omitempty"`
	ConfigMarker string   `json:"configMarker,omitempty"`
	Interpreter  string   `json:"interpreter,omitempty"`
	Skipped      bool     `json:"skipped,omitempty"`
	SitePackages []string `json:"sitePackages,omitempty"`
	ConfigError  string   `json:"configError,omitempty"`
}

// Inspect asks pyrefly (`pyrefly dump-config`) what config, Python
// interpreter and site-packages it resolves for file (or the project at
// root when file is ""). When interpreter is non-empty, it is passed via
// --python-interpreter-path so the reported site-packages match it.
func Inspect(ctx context.Context, bin, root, file, interpreter string) (EnvInfo, error) {
	if bin == "" {
		return EnvInfo{}, ErrNotFound
	}
	tctx, cancel := context.WithTimeout(ctx, inspectTimeout)
	defer cancel()
	args := []string{"dump-config", "--color=never", "--max-files", "0"}
	if interpreter != "" {
		args = append(args, "--python-interpreter-path", interpreter)
	}
	if file != "" {
		args = append(args, file)
	}
	cmd := cmdrun.Command(tctx, bin, args...)
	cmd.Dir = root
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	err := cmd.Run()
	info := ParseDumpConfig(stdout.String())
	if err != nil {
		if msg := dumpConfigError(stderr.String()); msg != "" {
			info.ConfigError = msg
			return info, nil
		}
		if info.Interpreter != "" || info.Config != "" || info.Skipped {
			return info, nil
		}
		return EnvInfo{}, err
	}
	return info, nil
}

// dumpConfigError returns the first configuration error line from
// `pyrefly dump-config`'s stderr, or "" for non-error messages such as
// "No Python files matched patterns".
func dumpConfigError(stderr string) string {
	hasFatal := strings.Contains(stderr, "Fatal configuration error") || strings.Contains(stderr, "TOML parse error")
	for _, line := range strings.Split(stderr, "\n") {
		s := strings.TrimSpace(line)
		if strings.HasPrefix(s, "ERROR ") {
			return strings.TrimSpace(strings.TrimPrefix(s, "ERROR "))
		}
		if hasFatal && strings.Contains(s, "parse error") {
			return s
		}
	}
	if hasFatal {
		return "Fatal configuration error"
	}
	return ""
}

// ParseDumpConfig parses the first configuration block printed by
// `pyrefly dump-config`.
func ParseDumpConfig(out string) EnvInfo {
	var info EnvInfo
	seenPkg := map[string]bool{}
	started := false
	for _, raw := range strings.Split(out, "\n") {
		line := strings.TrimRight(raw, "\r")
		trimmed := strings.TrimSpace(line)
		if trimmed == "" {
			continue
		}
		// Each configuration block starts at column 0. Stop after the first.
		isHeader := strings.HasPrefix(line, "Configuration at ") || strings.HasPrefix(line, "Default configuration")
		if isHeader {
			if started {
				break
			}
			started = true
			if rest, ok := strings.CutPrefix(line, "Configuration at "); ok {
				info.Config = unbacktick(rest)
			} else if rest, ok := strings.CutPrefix(line, "Default configuration for project root marked by "); ok {
				info.ConfigMarker = unbacktick(rest)
			}
			continue
		}
		if rest, ok := strings.CutPrefix(trimmed, "Using interpreter:"); ok {
			val := strings.TrimSpace(rest)
			switch {
			case val == "", strings.EqualFold(val, "none"), strings.EqualFold(val, "<none>"):
			case strings.HasPrefix(val, "<"):
				if strings.Contains(strings.ToLower(val), "skipped") {
					info.Skipped = true
				}
			default:
				info.Interpreter = filepath.Clean(val)
			}
			continue
		}
		for _, prefix := range []string{
			"Site packages path queried from interpreter:",
			"Site packages path from user:",
		} {
			if rest, ok := strings.CutPrefix(trimmed, prefix); ok {
				var paths []string
				if json.Unmarshal([]byte(strings.TrimSpace(rest)), &paths) == nil {
					for _, p := range paths {
						p = filepath.Clean(p)
						if p != "" && !seenPkg[p] {
							seenPkg[p] = true
							info.SitePackages = append(info.SitePackages, p)
						}
					}
				}
			}
		}
	}
	return info
}

func unbacktick(s string) string {
	s = strings.TrimSpace(s)
	s = strings.TrimPrefix(s, "`")
	s = strings.TrimSuffix(s, "`")
	if s == "" {
		return ""
	}
	return filepath.Clean(s)
}

// Version returns the first line of `<bin> --version` (e.g. "pyrefly 1.5.0"),
// or "" if it can't be read.
func Version(ctx context.Context, bin string) string {
	if bin == "" {
		return ""
	}
	tctx, cancel := context.WithTimeout(ctx, inspectTimeout)
	defer cancel()
	cmd := cmdrun.Command(tctx, bin, "--version")
	out, err := cmd.Output()
	if err != nil {
		return ""
	}
	first, _, _ := strings.Cut(string(out), "\n")
	return strings.TrimSpace(first)
}
