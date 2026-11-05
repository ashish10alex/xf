package engine

import (
	"context"
	"errors"
	"time"

	"github.com/ashish10alex/xf/internal/changes"
	"github.com/ashish10alex/xf/internal/deferral"
	"github.com/ashish10alex/xf/internal/project"
)

// Defer to prod (ADR 0031): the engine keeps the switch, for every client
// of the project, and works out which upstream actions to read from prod.

// Deferrer works out Deferrals for the engine; main makes one per tool.
type Deferrer interface {
	// Deferral is what defer to prod does with the upstream actions of
	// req.Actions in g, the compile on show. changed says which actions
	// are Changed Actions; nil when they aren't known.
	// With on false, defer is off and only the Proxy Views are looked up.
	Deferral(ctx context.Context, req DeferralRequest, g *project.Graph, changed func(project.ActionID) bool, on bool) (*DeferralResult, error)
	// Forget drops what is known of which tables exist: a run may just
	// have built some.
	Forget()
	// Unreadable remembers Prod Targets a query was denied, so the next
	// Deferral reads them from dev, and says whether any is new.
	Unreadable(ts []project.Target) bool
}

// DeferralRequest asks for the Deferral of some Selected Actions.
type DeferralRequest struct {
	// Actions are the Selected Actions: an action on show and what is shown
	// with it, or what a run builds.
	Actions []project.ActionID `json:"actions"`
	// ProdCompilerOptions are a Dataform project's prod compiler options
	// ("" for the default targets); nil for those xf serve reads from the
	// config files, for a client that has no settings of its own (xf lsp).
	ProdCompilerOptions *string `json:"prodCompilerOptions,omitempty"`
	// DefaultBranch is the branch Changed Actions are worked out against,
	// for the Stale Deferral flags; "" leaves them out.
	DefaultBranch string `json:"defaultBranch,omitempty"`
	// Proxies also lists the upstream actions' Dev Targets that are Proxy
	// Views, which a run reads prod through even with defer off. Only
	// they are looked up when defer is off.
	Proxies bool `json:"proxies,omitempty"`
	// Fresh drops what is known of which tables exist first, as a run does.
	Fresh bool `json:"fresh,omitempty"`
	// On turns defer to prod on or off for this request alone, as xf run's
	// --defer does (ADR 0038); nil follows the project's switch, which it
	// leaves as it is. A dbt project's compile follows the switch alone,
	// so a dbt Deferral that differs from it is nil, with DbtState.
	On *bool `json:"on,omitempty"`
}

// DeferralResult is the answer to a DeferralRequest.
type DeferralResult struct {
	// Deferral is nil when defer to prod is off.
	Deferral *deferral.Deferral `json:"deferral,omitempty"`
	// Proxies are the upstream Proxy Views, when asked for.
	Proxies []project.Target `json:"proxies,omitempty"`
	// DbtState is where a dbt run's --defer-state reads prod's manifest,
	// "" in a Dataform project.
	DbtState string `json:"dbtState,omitempty"`
	// StaleKnown says Changed Actions were known, so a deferred entry
	// without Stale isn't one. Without them, flags may come later.
	StaleKnown bool `json:"staleKnown,omitempty"`
}

// ErrNoDefer is returned by Deferral when the engine can't defer.
var ErrNoDefer = errors.New("defer to prod is not available for this project")

// SetDefer turns defer to prod on or off for every client, telling them,
// and saves the choice (Config.SaveDefer).
func (e *Engine) SetDefer(on bool) {
	if e.cfg.Defer == nil {
		return
	}
	changed := false
	e.updateIf(func(s State) (State, bool) {
		if s.Defer == on {
			return s, false
		}
		s.Defer = on
		changed = true
		return s, true
	})
	if changed && e.cfg.SaveDefer != nil {
		e.cfg.SaveDefer(on)
	}
	if changed && e.cfg.OnDefer != nil {
		e.cfg.OnDefer(on)
	}
}

// deferStaleWait bounds how long a run waits for Changed Actions to flag
// Stale Deferrals.
const deferStaleWait = 2 * time.Minute

// Deferral works out what defer to prod does with req's Selected Actions in
// the compile on show. With defer off it is nil, and only the Proxy Views
// are looked up, when asked. The Stale Deferral flags come from the
// Changed Actions the engine has; when it has none, they are worked out in
// the background (and every client told), unless req.Fresh, a run, which
// waits for them.
func (e *Engine) Deferral(ctx context.Context, req DeferralRequest) (*DeferralResult, error) {
	if e.cfg.Defer == nil {
		return nil, ErrNoDefer
	}
	e.mu.Lock()
	s := e.state
	e.mu.Unlock()
	if s.Graph == nil {
		return nil, errors.New("no compile yet")
	}
	if req.Fresh {
		e.cfg.Defer.Forget()
	}
	on := s.Defer
	if req.On != nil {
		on = *req.On
	}
	var changed func(project.ActionID) bool
	res := s.Changes
	if on && res == nil && req.DefaultBranch != "" && e.cfg.Changes != nil {
		creq := changes.Request{DefaultBranch: req.DefaultBranch, Compile: true}
		if req.Fresh {
			wctx, cancel := context.WithTimeout(ctx, deferStaleWait)
			res, _ = e.ChangedActions(wctx, creq)
			cancel()
		} else {
			go func() {
				ctx, cancel := context.WithTimeout(context.Background(), deferStaleWait)
				defer cancel()
				_, _ = e.ChangedActions(ctx, creq)
			}()
		}
	}
	if res != nil {
		ids := map[project.ActionID]bool{}
		for _, c := range res.Changed {
			ids[c.ID] = true
		}
		changed = func(id project.ActionID) bool { return ids[id] }
	}
	if !on && !req.Proxies {
		return &DeferralResult{}, nil
	}
	out, err := e.cfg.Defer.Deferral(ctx, req, s.Graph, changed, on)
	if err != nil {
		return nil, err
	}
	out.StaleKnown = changed != nil
	return out, nil
}

// DeferUnreadable remembers Prod Targets a query was denied (see
// deferral.AccessDenied), so the next Deferral reads them from dev. It
// says whether any is new.
func (e *Engine) DeferUnreadable(ts []project.Target) bool {
	if e.cfg.Defer == nil {
		return false
	}
	return e.cfg.Defer.Unreadable(ts)
}
