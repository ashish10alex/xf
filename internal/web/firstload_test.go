package web

import (
	"io/fs"
	"path"
	"regexp"
	"testing"
)

// The page's first load is main.js and every module it imports, directly
// or through others. The browser finds a module's imports only once the
// module arrives, so each level of the graph is another round of requests,
// and every module is one more request on the six connections a browser
// keeps to a host. These budgets make growing the load a decision: raise
// one on purpose, saying why in the commit, whose log is the budgets'
// history. The bytes budget keeps about 20 KB of slack so a small change
// fits: when one doesn't, raise it by 25 KB.
const (
	maxFirstLoadModules = 59
	maxFirstLoadBytes   = 1_650_000 // 1,628,756 on 2026-11-09
	maxFirstLoadDepth   = 4         // main.js is 1
)

// staticImport is an import or re-export of a module by path; a module
// imported with import() isn't in the first load.
var staticImport = regexp.MustCompile(`(?m)^\s*(?:import|export)\s+(?:[^'";]*?\s+from\s+)?['"](\.{1,2}/[^'"]+)['"]`)

func TestFirstLoadBudget(t *testing.T) {
	static, err := fs.Sub(assets, "assets")
	if err != nil {
		t.Fatal(err)
	}
	depth := map[string]int{"main.js": 1}
	queue := []string{"main.js"}
	bytes, deepest := 0, 1
	for len(queue) > 0 {
		mod := queue[0]
		queue = queue[1:]
		src, err := fs.ReadFile(static, mod)
		if err != nil {
			t.Fatalf("%s: %v", mod, err)
		}
		bytes += len(src)
		deepest = max(deepest, depth[mod])
		for _, m := range staticImport.FindAllStringSubmatch(string(src), -1) {
			dep := path.Join(path.Dir(mod), m[1])
			if _, ok := depth[dep]; !ok {
				depth[dep] = depth[mod] + 1
				queue = append(queue, dep)
			}
		}
	}
	t.Logf("first load: %d modules, %d bytes, %d levels", len(depth), bytes, deepest)
	if len(depth) > maxFirstLoadModules {
		t.Errorf("the first load fetches %d modules, budget %d", len(depth), maxFirstLoadModules)
	}
	if bytes > maxFirstLoadBytes {
		t.Errorf("the first load fetches %d bytes of modules, budget %d", bytes, maxFirstLoadBytes)
	}
	if deepest > maxFirstLoadDepth {
		t.Errorf("the first load's imports go %d levels deep, budget %d", deepest, maxFirstLoadDepth)
	}
}
