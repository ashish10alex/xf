package navigate

import (
	"os"
	"path/filepath"
	"regexp"
	"strings"
)

// requireCall matches require("path").
var requireCall = regexp.MustCompile(`\brequire\s*\(\s*["'` + "`" + `]([^"'` + "`" + `]+)["'` + "`" + `]\s*\)`)

// requireDefinition resolves a require() call around offset to the file it
// loads. Dataform resolves paths from the project root; ./ and ../ paths are
// relative to the requiring file.
func requireDefinition(p Project, file string, src []byte, offset int) ([]Location, bool) {
	for _, m := range requireCall.FindAllSubmatchIndex(src, -1) {
		if offset < m[0] || offset > m[1] {
			continue
		}
		if path := resolveRequire(p.Root, file, string(src[m[2]:m[3]])); path != "" {
			return []Location{{File: path}}, true
		}
		return nil, true
	}
	return nil, false
}

// resolveRequire returns the file a require() path loads, or "" if there is
// none.
func resolveRequire(root, from, spec string) string {
	base := root
	if strings.HasPrefix(spec, "./") || strings.HasPrefix(spec, "../") {
		base = filepath.Dir(from)
	}
	path := filepath.Join(base, filepath.FromSlash(spec))
	for _, candidate := range []string{path, path + ".js", filepath.Join(path, "index.js")} {
		if info, err := os.Stat(candidate); err == nil && !info.IsDir() {
			return candidate
		}
	}
	return ""
}
