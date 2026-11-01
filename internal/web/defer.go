package web

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/ashish10alex/xf/internal/config"
	"github.com/ashish10alex/xf/internal/deferral"
	"github.com/ashish10alex/xf/internal/engine"
	"github.com/ashish10alex/xf/internal/frontend"
	"github.com/ashish10alex/xf/internal/navigate"
	"github.com/ashish10alex/xf/internal/project"
)

// Defer to prod in xf web (ADR 0031). The engine keeps the switch and
// works out each Deferral; xf web asks for the actions it shows, dry runs
// and shows their SQL rewritten to read prod, lists what is read from
// prod in pane 2 and after each ref() in the source pane, and makes the
// Proxy Views a deferred run needs (defer_run.go).

// deferMaxAge is how long a Deferral is used before it is asked for
// again; while it is asked, the old one is used.
const deferMaxAge = 2 * time.Minute

// deferTimeout bounds asking for a Deferral: the first may compile the
// project with prod compiler options.
const deferTimeout = 2 * time.Minute

// deferWeb is what xf web knows of defer to prod.
type deferWeb struct {
	// byAction is each action's Deferral of its own upstream tables, nil
	// until asked for.
	byAction map[project.ActionID]*deferKnown
	// asking are the requests in flight, by their actions.
	asking map[string]bool
	// gen goes up when what is known is dropped, so an answer to an older
	// request is not kept.
	gen int
	// err is why the last Deferral was not worked out.
	err string
}

type deferKnown struct {
	// d is nil when the Deferral failed (deferWeb.err): the action is dry
	// run as compiled.
	d  *deferral.Deferral
	at time.Time
}

// deferOnLocked says whether defer to prod is on, so xf web lists what it
// reads from prod.
func (p *projectState) deferOnLocked() bool {
	return p.deps.Deferral != nil && p.st.Defer
}

// deferRewritesLocked says whether xf web rewrites what it dry runs and
// shows to read prod: in a Dataform project. dbt's own --defer compiles
// prod's names in.
func (p *projectState) deferRewritesLocked() bool {
	return p.deferOnLocked() && p.tool == "dataform"
}

// deferResetLocked drops every Deferral, as turning defer on or off does.
func (p *projectState) deferResetLocked() {
	p.def.byAction = map[project.ActionID]*deferKnown{}
	p.def.asking = map[string]bool{}
	p.def.err = ""
	p.def.gen++
}

// deferExpireLocked has every Deferral asked for again when next shown,
// using it until then: after a compile, a run or new Changed Actions. An
// answer on its way is still kept.
func (p *projectState) deferExpireLocked() {
	for _, k := range p.def.byAction {
		k.at = time.Time{}
	}
	p.def.asking = map[string]bool{}
}

// prodOptionsLocked are the prod compiler options a Deferral is worked out
// with.
func (p *projectState) prodOptionsLocked() *string {
	o := p.deps.ProdCompilerOptions
	return &o
}

// deferStartsLocked has the Deferral of actions asked for when it isn't
// known or is old. ready says each one's is known, so they can be dry run.
func (p *projectState) deferStartsLocked(actions []*project.Action) (ready bool, starts []func()) {
	if !p.deferOnLocked() {
		return true, nil
	}
	now := p.deps.Now()
	ready = true
	need := false
	ids := make([]string, 0, len(actions))
	req := engine.DeferralRequest{ProdCompilerOptions: p.prodOptionsLocked(), DefaultBranch: p.base}
	for _, a := range actions {
		ids = append(ids, string(a.ID))
		req.Actions = append(req.Actions, a.ID)
		k := p.def.byAction[a.ID]
		if k == nil {
			ready = false
		}
		if k == nil || now.Sub(k.at) >= deferMaxAge {
			need = true
		}
	}
	if !p.deferRewritesLocked() {
		ready = true // nothing waits for it
	}
	key := strings.Join(ids, "\x00")
	if !need || len(actions) == 0 || p.def.asking[key] {
		return ready, nil
	}
	p.def.asking[key] = true
	gen := p.def.gen
	ask := p.deps.Deferral
	return ready, []func(){func() {
		ctx, cancel := context.WithTimeout(context.Background(), deferTimeout)
		defer cancel()
		res, err := ask(ctx, req)
		p.mu.Lock()
		if gen == p.def.gen {
			if p.def.asking[key] {
				delete(p.def.asking, key)
			}
			p.def.err = ""
			if err != nil {
				p.def.err = err.Error()
			}
			at := p.deps.Now()
			for _, a := range actions {
				k := &deferKnown{at: at}
				if err == nil && res.Deferral != nil {
					k.d = res.Deferral.For(a)
				}
				p.def.byAction[a.ID] = k
			}
		}
		p.mu.Unlock()
		p.hub.send("detail", nil)
	}}
}

// deferredLocked is a as xf web dry runs and shows it: reading its
// Deferred Actions from prod when defer to prod is on.
func (p *projectState) deferredLocked(a *project.Action) *project.Action {
	if !p.deferRewritesLocked() {
		return a
	}
	if k := p.def.byAction[a.ID]; k != nil && k.d != nil {
		return deferral.RewriteAction(a, k.d.Entries)
	}
	return a
}

// deferView is pane 2's list of what defer to prod does with the upstream
// tables of the action on show.
type deferView struct {
	Loading bool                 `json:"loading,omitempty"`
	Error   string               `json:"error,omitempty"`
	Rows    []frontend.DeferHint `json:"rows,omitempty"`
	// Stale says some are read from prod but changed on this branch.
	Stale bool `json:"stale,omitempty"`
}

// deferViewLocked is a's list; nil while defer to prod is off.
func (p *projectState) deferViewLocked(g *project.Graph, a *project.Action) *deferView {
	if !p.deferOnLocked() {
		return nil
	}
	k := p.def.byAction[a.ID]
	if k == nil {
		return &deferView{Loading: true}
	}
	if k.d == nil {
		return &deferView{Error: cmpOr(p.def.err, "not worked out")}
	}
	v := &deferView{}
	order, hints := frontend.DeferHints(g, a, k.d, p.deps.Now())
	for _, id := range order {
		h := hints[id]
		v.Rows = append(v.Rows, h)
		v.Stale = v.Stale || strings.HasPrefix(h.Label, "⚠ prod")
	}
	return v
}

// deferRequest turns defer to prod on or off.
type deferRequest struct {
	On bool `json:"on"`
}

// deferAnswer says what turning it on or off did, and what to ask next.
type deferAnswer struct {
	OK      bool   `json:"ok"`
	Message string `json:"message,omitempty"`
	// Prod: the prodCompilerOptions prompt goes first.
	Prod bool `json:"prod,omitempty"`
	// Proxies are the Proxy Views left in dev datasets, which turning
	// defer off offers to remove.
	Proxies []string `json:"proxies,omitempty"`
}

// setDefer turns defer to prod on or off for every client of the engine.
func (p *projectState) setDefer(w http.ResponseWriter, r *http.Request) {
	var req deferRequest
	if !readJSON(w, r, &req) {
		return
	}
	if p.deps.SetDefer == nil {
		writeError(w, errors.New("defer to prod is not available in this project"))
		return
	}
	p.mu.Lock()
	if req.On && p.tool == "dataform" && p.askProdLocked() {
		p.mu.Unlock()
		writeJSON(w, deferAnswer{Prod: true})
		return
	}
	g := p.st.Graph
	p.mu.Unlock()
	p.deps.SetDefer(req.On)
	ans := deferAnswer{OK: true, Message: "defer to prod is off"}
	if req.On {
		ans.Message = "defer to prod is on: upstream tables not built in dev are read from prod"
		if p.tool == "dbt" {
			ans.Message += " (dbt --defer; compiling again)"
		}
	} else if g != nil && p.deps.DeferWriter != nil && p.tool == "dataform" {
		ctx, cancel := context.WithTimeout(r.Context(), 30*time.Second)
		defer cancel()
		if bq, err := p.deps.DeferWriter(ctx); err == nil {
			if found, err := deferral.RemoveProxies(ctx, bq, g, false); err == nil {
				for _, t := range found {
					ans.Proxies = append(ans.Proxies, string(t.ID()))
				}
			}
		}
	}
	writeJSON(w, ans)
}

// removeProxies removes the project's Proxy Views.
func (p *projectState) removeProxies(w http.ResponseWriter, r *http.Request) {
	p.mu.Lock()
	g := p.st.Graph
	p.mu.Unlock()
	if g == nil || p.deps.DeferWriter == nil {
		writeError(w, errors.New("there are no proxy views to remove here"))
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 2*time.Minute)
	defer cancel()
	bq, err := p.deps.DeferWriter(ctx)
	if err != nil {
		writeError(w, err)
		return
	}
	removed, err := deferral.RemoveProxies(ctx, bq, g, true)
	if forget := p.deps.ForgetFailedDryRuns; forget != nil {
		forget() // the engine looks the dev tables up again
	}
	p.mu.Lock()
	p.deferExpireLocked()
	p.mu.Unlock()
	p.hub.send("detail", nil)
	if err != nil {
		writeError(w, fmt.Errorf("removed %d, then: %w", len(removed), err))
		return
	}
	msg := "no proxy views to remove"
	if len(removed) > 0 {
		msg = "removed " + plural(len(removed), "proxy view")
	}
	writeJSON(w, okView{OK: true, Message: msg})
}

// refLabel is a label after a ref() in the source pane.
type refLabel struct {
	// Line counts from 0; Col is the UTF-16 column the ref() ends at.
	Line int `json:"line"`
	Col  int `json:"col"`
	frontend.DeferHint
}

// refLabelsView answers /api/defer/refs.
type refLabelsView struct {
	On      bool       `json:"on"`
	Pending bool       `json:"pending,omitempty"`
	Labels  []refLabel `json:"labels"`
}

// refLabels are the labels after the ref() calls of ?file, as it is on
// disk: what defer to prod reads each from. While a Deferral of the
// file's actions is being worked out it says it is pending, and the page
// asks again at the next detail event.
func (p *projectState) refLabels(w http.ResponseWriter, r *http.Request) {
	file := r.URL.Query().Get("file")
	p.mu.Lock()
	g := p.st.Graph
	if !p.deferOnLocked() || g == nil || file == "" {
		p.mu.Unlock()
		writeJSON(w, refLabelsView{Labels: []refLabel{}})
		return
	}
	actions := g.ActionsInFile(file)
	ready, starts := p.deferStartsLocked(actions)
	type known struct {
		a *project.Action
		d *deferral.Deferral
	}
	var ks []known
	for _, a := range actions {
		if k := p.def.byAction[a.ID]; k != nil && k.d != nil {
			ks = append(ks, known{a, k.d})
		}
	}
	now := p.deps.Now()
	p.mu.Unlock()
	for _, s := range starts {
		go s()
	}
	v := refLabelsView{On: true, Pending: !ready, Labels: []refLabel{}}
	if len(ks) == 0 {
		writeJSON(w, v)
		return
	}
	hints := map[project.ActionID]frontend.DeferHint{}
	for _, k := range ks {
		_, hs := frontend.DeferHints(g, k.a, k.d, now)
		for id, h := range hs {
			hints[id] = h
		}
	}
	abs := filepath.Join(p.deps.Root, filepath.FromSlash(file))
	src, err := os.ReadFile(abs)
	if err != nil {
		writeJSON(w, v)
		return
	}
	lines := strings.Split(string(src), "\n")
	starts2 := make([]int, len(lines))
	at := 0
	for i, l := range lines {
		starts2[i] = at
		at += len(l) + 1
	}
	seen := map[int]bool{}
	for _, ref := range navigate.TableRefs(navigate.Project{Root: p.deps.Root, Graph: g, Dbt: p.tool == "dbt"}, abs, src) {
		if ref.Source != navigate.SourceRef || seen[ref.End] {
			continue
		}
		h, ok := hints[ref.Target.ID()]
		if !ok {
			continue
		}
		seen[ref.End] = true
		end := afterRefClose(src, ref.End)
		line := lineOf(starts2, end)
		col := unitsAt(lines[line], end-starts2[line])
		v.Labels = append(v.Labels, refLabel{Line: line, Col: col, DeferHint: h})
	}
	writeJSON(w, v)
}

// afterRefClose is where a label after the ref() ending at end goes:
// past the } of a sqlx ${ref()}, or the }} of a dbt {{ ref() }}, when
// one follows on the same line.
func afterRefClose(src []byte, end int) int {
	i := end
	for i < len(src) && (src[i] == ' ' || src[i] == '\t') {
		i++
	}
	switch {
	case i+1 < len(src) && src[i] == '}' && src[i+1] == '}':
		return i + 2
	case i < len(src) && src[i] == '}' && i == end:
		return i + 1
	}
	return end
}

// lineOf is the line that offset is on, given each line's start.
func lineOf(starts []int, offset int) int {
	line := 0
	for i, s := range starts {
		if s > offset {
			break
		}
		line = i
	}
	return line
}

// deferCaps says whether xf web shows defer to prod.
func (p *projectState) deferCaps() bool { return p.deps.SetDefer != nil && p.deps.Deferral != nil }

// deferUnset is the setting defer to prod needs that nothing sets, for
// the status line's hover: "" when none.
func (p *projectState) deferUnsetLocked() string {
	if p.tool == "dbt" && p.deps.DbtProdTarget == "" {
		return config.KeyDbtProdTarget
	}
	return ""
}

// deferredFile is the Deferral of each action of file, for the source
// pane's dry run of it: asked for and waited on when it isn't known. Nil
// while defer to prod is off.
func (p *projectState) deferredFile(ctx context.Context, g *project.Graph, file string) map[project.ActionID]*deferral.Deferral {
	p.mu.Lock()
	if !p.deferRewritesLocked() {
		p.mu.Unlock()
		return nil
	}
	actions := g.ActionsInFile(file)
	ready, starts := p.deferStartsLocked(actions)
	p.mu.Unlock()
	for _, s := range starts {
		go s()
	}
	// Until each is known, which another request may be asking for.
	for !ready && ctx.Err() == nil {
		time.Sleep(50 * time.Millisecond)
		p.mu.Lock()
		ready = true
		for _, a := range actions {
			ready = ready && p.def.byAction[a.ID] != nil
		}
		p.mu.Unlock()
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	out := map[project.ActionID]*deferral.Deferral{}
	for _, a := range actions {
		if k := p.def.byAction[a.ID]; k != nil && k.d != nil {
			out[a.ID] = k.d
		}
	}
	return out
}
