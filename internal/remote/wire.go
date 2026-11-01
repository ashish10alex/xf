// Package remote shares one project engine (see package engine) between
// processes. `xf serve` runs the engine and listens on a per-project Unix
// socket; the TUI and `xf lsp` connect as clients, starting a server when
// none is listening and starting a new one if theirs goes away. Messages
// are JSON-RPC, framed as in LSP (see package jsonrpc).
//
// A client first says which xf build it is (xf/hello): a server of an
// older build stops, so the client starts one of its own, which keeps an
// engine from outliving an upgrade. A client is sent the engine's state as
// soon as it connects and whenever it changes (xf/state), and the file
// active in the editor whenever an editor reports one (xf/activeFile). It
// may ask for a compile (xf/recompile), have one action compiled ahead of
// the rest (xf/focus), dry-run SQL (xf/dryRun) and have failed dry runs tried again
// (xf/forgetFailedDryRuns), look a table up in BigQuery (xf/table), work out
// Changed Actions (xf/changedActions) and their column impact
// (xf/columnImpact, which reports progress with xf/impactProgress) and
// their query cost (xf/queryCost, with xf/costProgress), turn defer to
// prod on or off (xf/setDefer) and ask what it reads from prod
// (xf/deferral, xf/deferUnreadable), report
// the active file (xf/activeFile), and have the editor that reported one
// last open a file (xf/showDocument, which the server passes on to that
// editor's client as a request of its own).
package remote

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/ashish10alex/xf/internal/bigquery"
	"github.com/ashish10alex/xf/internal/changes"
	"github.com/ashish10alex/xf/internal/cost"
	"github.com/ashish10alex/xf/internal/engine"
	"github.com/ashish10alex/xf/internal/impact"
	"github.com/ashish10alex/xf/internal/project"
)

// Methods.
const (
	// methodHello is a client's first message on every connection: its
	// helloParams.
	methodHello      = "xf/hello"
	methodState      = "xf/state"
	methodActiveFile = "xf/activeFile"
	methodRecompile  = "xf/recompile"
	// methodFocus takes focusParams (see engine.Engine.Focus).
	methodFocus = "xf/focus"
	// methodSetTheme takes setThemeParams; the server sets the engine's
	// theme and every client is told with the next xf/state.
	methodSetTheme = "xf/setTheme"
	methodDryRun   = "xf/dryRun"
	// methodForgetFailedDryRuns has the engine try failed dry runs again
	// (see engine.Engine.ForgetFailedDryRuns); it takes no params.
	methodForgetFailedDryRuns = "xf/forgetFailedDryRuns"
	// methodTable takes tableParams and answers with a *bigquery.Table,
	// failing as a dry run does (see dryRunError).
	methodTable = "xf/table"
	// methodChangedActions takes a changes.Request and answers with a
	// *changes.Result (null when the base isn't compiled and the request
	// didn't allow compiling it).
	methodChangedActions = "xf/changedActions"
	// methodColumnImpact takes columnImpactParams and answers with an
	// *impact.Report, telling the client how far it got with
	// methodImpactProgress notifications carrying the request's token.
	methodColumnImpact   = "xf/columnImpact"
	methodImpactProgress = "xf/impactProgress"
	// methodQueryCost takes queryCostParams and answers with a
	// *cost.Report, telling the client how far it got with
	// methodCostProgress notifications carrying the request's token.
	methodQueryCost    = "xf/queryCost"
	methodCostProgress = "xf/costProgress"
	// methodShowDocument takes showDocumentParams: from a client, it asks
	// the server to have the editor that last sent xf/activeFile open a
	// file; the server asks that editor's client with the same method.
	// Either answers codeNoEditor when there is no editor to ask.
	methodShowDocument = "xf/showDocument"
	// methodSetDefer takes setDeferParams: the server turns defer to prod
	// on or off and every client is told with the next xf/state.
	methodSetDefer = "xf/setDefer"
	// methodDeferral takes an engine.DeferralRequest and answers with an
	// *engine.DeferralResult (see engine.Engine.Deferral).
	methodDeferral = "xf/deferral"
	// methodDeferUnreadable takes deferUnreadableParams and answers with
	// whether any of its tables is new (see engine.Engine.DeferUnreadable).
	methodDeferUnreadable = "xf/deferUnreadable"
)

// codeNoEditor is xf/showDocument's error when no editor can open the file.
const codeNoEditor = -32001

// ErrNoEditor means no editor has said which file is active, or the one
// that did can't open files for xf.
var ErrNoEditor = errors.New("no editor connected")

// wireState is engine.State on the wire. The graph travels as its compile
// JSON, and only when it differs from the last one sent on the connection.
type wireState struct {
	Compiling bool      `json:"compiling"`
	Reason    string    `json:"reason,omitempty"`
	Started   time.Time `json:"started"`
	Compiles  int       `json:"compiles"`
	// HasGraph says whether the server has a graph; Graph is its compile
	// JSON when it is new to this connection.
	HasGraph   bool                       `json:"hasGraph"`
	Graph      json.RawMessage            `json:"graph,omitempty"`
	CompiledAt time.Time                  `json:"compiledAt"`
	Took       time.Duration              `json:"took"`
	Errors     []project.CompilationError `json:"errors,omitempty"`
	Err        string                     `json:"err,omitempty"`
	Stale      bool                       `json:"stale"`
	Saved      bool                       `json:"saved,omitempty"`
	Outdated   string                     `json:"outdated,omitempty"`
	Partial    bool                       `json:"partial,omitempty"`
	Focus      project.ActionID           `json:"focus,omitempty"`
	WatchErr   string                     `json:"watchErr,omitempty"`
	// Changes are the live Changed Actions, sent with every state.
	Changes         *changes.Result `json:"changes,omitempty"`
	ImpactSignature string          `json:"impactSignature,omitempty"`
	Theme           string          `json:"theme,omitempty"`
	Defer           bool            `json:"defer,omitempty"`
}

func errText(err error) string {
	if err == nil {
		return ""
	}
	return err.Error()
}

func textErr(s string) error {
	if s == "" {
		return nil
	}
	return errors.New(s)
}

// Build identifies the xf build a process runs: its executable's
// modification time and size. The zero Build is unknown, and is never
// compared.
type Build struct {
	Time time.Time `json:"time"`
	Size int64     `json:"size"`
}

// IsZero reports whether b is unknown.
func (b Build) IsZero() bool { return b.Time.IsZero() && b.Size == 0 }

// newer reports whether b is a later build than old: built later, or, at
// the same time, bigger. Builds of either kind that are unknown are
// neither newer nor older.
func (b Build) newer(old Build) bool {
	if b.IsZero() || old.IsZero() {
		return false
	}
	if !b.Time.Equal(old.Time) {
		return b.Time.After(old.Time)
	}
	return b.Size > old.Size
}

func (b Build) String() string {
	return fmt.Sprintf("built %s, %d bytes", b.Time.Format(time.DateTime), b.Size)
}

type helloParams struct {
	Build Build `json:"build"`
}

type recompileParams struct {
	Reason string `json:"reason"`
}

type setThemeParams struct {
	Theme string `json:"theme"`
}

type setDeferParams struct {
	On bool `json:"on"`
}

type deferUnreadableParams struct {
	Targets []project.Target `json:"targets"`
}

type focusParams struct {
	Action project.ActionID `json:"action"`
}

type activeFileParams struct {
	Event string `json:"event"`
	// File is an absolute path from an editor, and relative to the project
	// root from the server.
	File string `json:"file"`
}

type showDocumentParams struct {
	// File is an absolute path.
	File string `json:"file"`
	// Line is 0-based.
	Line int `json:"line"`
}

type columnImpactParams struct {
	engine.ImpactRequest
	// Token tells this request's progress notifications from others'.
	Token int64 `json:"token"`
}

type impactProgressParams struct {
	Token int64 `json:"token"`
	impact.Progress
}

type queryCostParams struct {
	engine.CostRequest
	// Token tells this request's progress notifications from others'.
	Token int64 `json:"token"`
}

type costProgressParams struct {
	Token int64 `json:"token"`
	cost.Progress
}

type dryRunParams struct {
	Project  string `json:"project"`
	Location string `json:"location"`
	SQL      string `json:"sql"`
}

type tableParams struct {
	Project string `json:"project"`
	Dataset string `json:"dataset"`
	Table   string `json:"table"`
}

// dryRunError is a failed dry run's error in a form the client can turn
// back into one callers can inspect with errors.Is and errors.As.
type dryRunError struct {
	Kind    string `json:"kind"` // api, auth, timeout, off or other
	Status  int    `json:"status,omitempty"`
	Message string `json:"message,omitempty"`
}

func encodeDryRunError(err error) dryRunError {
	var api *bigquery.APIError
	switch {
	case errors.As(err, &api):
		return dryRunError{Kind: "api", Status: api.Status, Message: api.Message}
	case errors.Is(err, bigquery.ErrAuth):
		return dryRunError{Kind: "auth", Message: strings.TrimPrefix(err.Error(), bigquery.ErrAuth.Error()+": ")}
	case errors.Is(err, context.DeadlineExceeded):
		return dryRunError{Kind: "timeout"}
	case errors.Is(err, engine.ErrNoDryRun):
		return dryRunError{Kind: "off"}
	}
	return dryRunError{Kind: "other", Message: err.Error()}
}

func (e dryRunError) err() error {
	switch e.Kind {
	case "api":
		return &bigquery.APIError{Status: e.Status, Message: e.Message}
	case "auth":
		return fmt.Errorf("%w: %s", bigquery.ErrAuth, e.Message)
	case "timeout":
		return fmt.Errorf("dry run: %w", context.DeadlineExceeded)
	case "off":
		return engine.ErrNoDryRun
	}
	return errors.New(e.Message)
}
