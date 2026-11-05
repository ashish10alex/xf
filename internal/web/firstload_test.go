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
// one on purpose, saying why in the commit.
const (
	maxFirstLoadModules = 57        // 39 since state.js was split by feature; 43 with the declarations table; 45 with Search; 47 with the merge request pill (forge.js); 49 with pane 2's target section; 50 with the page log (diag.js); 51 with the right-click menu the file tree and the source pane share (ctx-menu.js); 52 with gf and ctrl+x ctrl+f's file names (source-paths.js); 53 with gc and cmd+/'s comments (comment.js); 54 with ctrl+q's exits (exit.js); 55 with the vimMode setting's words for saving, discarding and undoing (vim-mode.js); 57 with the prod settings as forms (prod-options.js, prod-form-view.js)
	maxFirstLoadBytes   = 1_592_000 // 1,590,000 before the projects tab's Close Project and its dialog (tree.js, state.js, view.js); 1,586,000 before a card the mouse showed took the keys on a click, and cmd+click went to the definition (scratch-complete.js's keyOnFocus, source-smart.js, view.js's help); 1,582,000 before a new worktree asked its name, xf_ first (branches.js, view.js); 1,580,000 before ctrl+shift+↑/↓ added a cursor without vim, and the cursor line followed the main cursor alone (editor.js, view.js's help); 1,578,000 before the table card listed a record's fields and its keys took u and y (scratch-complete.js, view.js's help); 1,575,000 before the card K shows took the keys: f filters, j/k scroll, esc goes back (scratch-complete.js's keyedCard, source-smart.js, view.js's help); 1,572,000 before Source Control's rows were tidied: the commits' author badges and merges, a draft merge request said once, the commit box dimmed on a clean tree and its empty sections left out (scm.js, scm-view.js, forge.js, forge-view.js); 1,560,000 before the picker found the projects in a folder, as xf search does (picker.js, view.js; it is the first screen); 1,551,000 before the prod settings as forms (prod-options.js, prod-form-view.js; the dialog in view.js, the rows in settings-view.js); 1,549,000 before defer to prod's card marked the words that differ between the dev and prod names (diff.js's wordMarks, source-smart.js); 1,547,000 before cmd+shift+e, which shows the open file in the file tree (tree.js, state.js, main.js, view.js); 1,544,000 before the rail of icons (tree-view.js, tree.js; the corner gear left view.js); 1,532,000 before the vimMode setting (vim-mode.js; editor.js's switch and esc; the palette's Go to: and Buffers: commands in source-editor.js; the menu's next/prev change and error; the help without vim in view.js; the Settings Vim tab's note); 1,530,000 before cmd+b centred the open file's row in the file tree (tree.js, tree-view.js); 1,529,000 before the dependency inspector's page was kept (its workspace part and go back command in state.js, its z in view.js's help); 1,526,000 before the dependency inspector's hooks and Scratch's results taking a view (state.js, view.js, exit.js, scratch.js, scratch-view.js; the page itself loads when it first opens); 1,524,000 before removing a worktree anyway, losing its changes (state.js, view.js); 1,522,000 before the remove worktree dialog (view.js, state.js); 1,518,000 before the projects tab's right-click menu and New Worktree… (tree.js, branches.js, view.js); 1,515,000 before the projects tab listed the worktrees that aren't open (picker.js, tree.js); 1,506,000 before worktrees: B's shift+enter and the projects lists' nesting and remove (branches.js, picker.js, state.js, view.js); 1,505,000 before a file with no action took the action a compile gives it with the file tree hidden (state.js); 1,500,000 before defer to prod's hover cards on a ref() and its label (source-smart.js); 1,490,000 before defer to prod (its switch, run questions and pane 2 list in checks.js and view.js, the ref labels in source-smart.js); 1,485,000 before the Scratch panels' drag handles (scratch.js, scratch-view.js); 1,475,000 before gc and cmd+/ (comment.js); 1,470,000 before the Scratch results' SQL tab (scratch-view.js, scratch-editor.js); 1,350,000 before the merge request pill; 1,400,000 before the target section; 1,420,000 before the file tree's menu; 1,425,000 before Terraform's highlighting in sqlx.js; 1,430,000 before gf and ctrl+x ctrl+f (source-paths.js); 1,445,000 before Format file (:format, in editor.js and the editors); 1,450,000 before the Markdown preview (its hooks; markdown.js itself loads with the first .md file); 1,455,000 before the palette's open projects list; 1,460,000 before the pyrefly config line in the Settings (settings.js, settings-view.js)
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
