package web

import (
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/ashish10alex/xf/internal/diagnose"
)

// fix is the command the errors tab offers for a failing compile, or the
// log for a failed run, which the terminal tab runs: Why says what is
// wrong.
type fix struct {
	Why     string `json:"why"`
	Command string `json:"command"`
	// core says the command is dataform install, after which the project
	// compiles again.
	core bool
}

// fixLocked is the fix for the compile's error, nil for none: install
// the CLI it can't find (missing), or install the project's
// @dataform/core when the CLI asks for that.
func (p *projectState) fixLocked(missing bool) *fix {
	if p.st.Err == nil {
		return nil
	}
	if missing {
		return &fix{Why: p.tool + " isn't installed", Command: p.install.Command}
	}
	if p.tool != "dataform" || !diagnose.NeedsCoreInstall(p.st.Err.Error()) {
		return nil
	}
	// dataform install installs package.json's packages: without one,
	// workflow_settings.yaml's dataformCoreVersion is what to change, as
	// the error says.
	if _, err := os.Stat(filepath.Join(p.deps.Root, "package.json")); err != nil {
		return nil
	}
	return &fix{Why: "the project's @dataform/core is missing or doesn't match the CLI", Command: p.dataformCommand("install"), core: true}
}

// runFixLocked is the fix for the run that just failed, from its log, nil
// for none: dataform init-creds when the project has no credentials file.
func (p *projectState) runFixLocked() *fix {
	if p.tool != "dataform" || p.run.api {
		return nil
	}
	for _, l := range p.run.lines {
		if diagnose.NeedsInitCreds(l.Text) {
			return &fix{Why: "dataform has no credentials for this project (.df-credentials.json)", Command: p.dataformCommand("init-creds")}
		}
	}
	return nil
}

// dataformCommand is dataform with args, run with the dataform the
// compile ran: by its name when PATH finds that one.
func (p *projectState) dataformCommand(args string) string {
	bin := ""
	if p.deps.Info != nil {
		bin = p.deps.Info.ToolBin
	}
	if bin == "" || samePath(lookPath("dataform"), bin) {
		return "dataform " + args
	}
	return shellQuote(bin) + " " + args
}

func lookPath(name string) string {
	p, _ := exec.LookPath(name)
	return p
}

// samePath says a and b are the same file, through symlinks.
func samePath(a, b string) bool {
	if a == "" || b == "" {
		return false
	}
	ra, err1 := filepath.EvalSymlinks(a)
	rb, err2 := filepath.EvalSymlinks(b)
	return err1 == nil && err2 == nil && ra == rb
}

// shellQuote quotes s for a POSIX shell when it needs it.
func shellQuote(s string) string {
	if s != "" && !strings.ContainsAny(s, " \t\n'\"\\$`!*?[]{}()<>|&;#~") {
		return s
	}
	return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'"
}

// termFix is POST /api/term/fix: the errors tab's fix, or with run the
// log's, run in the terminal tab.
func (p *projectState) termFix(w http.ResponseWriter, r *http.Request) {
	if !termSupported {
		http.Error(w, "the terminal tab needs macOS or Linux", http.StatusNotFound)
		return
	}
	var req struct {
		Run bool `json:"run"`
	}
	if !readJSON(w, r, &req) {
		return
	}
	missing := p.cliMissing()
	p.mu.Lock()
	var f *fix
	if req.Run {
		for _, l := range p.run.lines {
			if l.Fix != nil {
				f = l.Fix
			}
		}
	} else {
		f = p.fixLocked(missing)
	}
	p.mu.Unlock()
	if f == nil {
		http.Error(w, "nothing to fix", http.StatusConflict)
		return
	}
	if f.core {
		p.mu.Lock()
		p.coreFix = &coreFix{before: coreStamp(p.deps.Root)}
		p.mu.Unlock()
	}
	if err := p.terminal().run(f.Command); err != nil {
		writeError(w, err)
		return
	}
	writeJSON(w, okView{OK: true})
}

// coreFix is a dataform install run from the errors tab, with what the
// project's @dataform/core was before it.
type coreFix struct{ before string }

// coreStamp is the installed @dataform/core's package.json, by time and
// size: "" when there is none.
func coreStamp(root string) string {
	fi, err := os.Stat(filepath.Join(root, "node_modules", "@dataform", "core", "package.json"))
	if err != nil {
		return ""
	}
	return fi.ModTime().String() + "/" + strconv.FormatInt(fi.Size(), 10)
}

// recompileAfterFix compiles the project again once dataform install,
// run from the errors tab, has changed the installed @dataform/core. The
// terminal says the shell is back at its prompt, which it also says as
// the command is typed.
func (p *projectState) recompileAfterFix() {
	p.mu.Lock()
	f := p.coreFix
	p.mu.Unlock()
	if f == nil || coreStamp(p.deps.Root) == f.before {
		return
	}
	p.mu.Lock()
	again := p.coreFix == f
	p.coreFix = nil
	p.mu.Unlock()
	if again {
		p.deps.Engine.Recompile("dataform install is done")
	}
}
