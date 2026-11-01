package pyrefly

import (
	"encoding/json"
	"net/url"
	"path/filepath"
	"runtime"
	"strings"
)

// Position is a place in a file: line counts from 0, Character is a
// UTF-16 column in it, as LSP and JavaScript count.
type Position struct {
	Line      int `json:"line"`
	Character int `json:"character"`
}

// Range is from Start to End, End not in it.
type Range struct {
	Start Position `json:"start"`
	End   Position `json:"end"`
}

// Diagnostic is one of pyrefly's errors in a file.
type Diagnostic struct {
	Range Range `json:"range"`
	// Severity is "error", "warning", "info" or "hint".
	Severity string `json:"severity"`
	Message  string `json:"message"`
	// Code is pyrefly's name for the kind of error, e.g. bad-assignment.
	Code string `json:"code,omitempty"`
}

// Hover is what pyrefly says of the symbol at a place: Markdown, and the
// range of the symbol, nil when it doesn't say.
type Hover struct {
	Markdown string `json:"markdown"`
	Range    *Range `json:"range,omitempty"`
}

// Location is a place in a file, by its absolute path.
type Location struct {
	Path  string `json:"path"`
	Range Range  `json:"range"`
}

// Completion is something to type at a place: Insert replaces Range.
type Completion struct {
	Label string `json:"label"`
	// Kind is the LSP completion kind's name, lower case: "function",
	// "variable", "module", "keyword"…; "" when pyrefly gives none.
	Kind   string `json:"kind,omitempty"`
	Detail string `json:"detail,omitempty"`
	Insert string `json:"insert"`
	// Range is what Insert replaces; nil is the word before the cursor.
	Range  *Range `json:"range,omitempty"`
	Filter string `json:"filter,omitempty"`
	Sort   string `json:"sort,omitempty"`
}

// Signature is the call being written at a place: its label, its
// documentation, where each parameter is in the label (UTF-16 offsets),
// and which parameter the cursor is on, -1 for none.
type Signature struct {
	Label  string   `json:"label"`
	Doc    string   `json:"doc,omitempty"`
	Params [][2]int `json:"params"`
	Active int      `json:"active"`
}

// ---- LSP's own shapes ----

type lspDiagnostic struct {
	Range    Range           `json:"range"`
	Severity int             `json:"severity"`
	Code     json.RawMessage `json:"code"`
	Message  string          `json:"message"`
}

type publishParams struct {
	URI         string          `json:"uri"`
	Version     *int            `json:"version"`
	Diagnostics []lspDiagnostic `json:"diagnostics"`
}

var severities = map[int]string{1: "error", 2: "warning", 3: "info", 4: "hint"}

func (d lspDiagnostic) diagnostic() Diagnostic {
	out := Diagnostic{Range: d.Range, Severity: severities[d.Severity], Message: d.Message}
	if out.Severity == "" {
		out.Severity = "error"
	}
	var s string
	if json.Unmarshal(d.Code, &s) == nil {
		out.Code = s
	} else if len(d.Code) > 0 && string(d.Code) != "null" {
		out.Code = string(d.Code)
	}
	return out
}

// markdownOf is the Markdown of LSP's hover contents and documentation:
// a MarkupContent, a MarkedString ({language, value} or a string), or a
// list of MarkedStrings.
func markdownOf(raw json.RawMessage) string {
	if len(raw) == 0 || string(raw) == "null" {
		return ""
	}
	var s string
	if json.Unmarshal(raw, &s) == nil {
		return s
	}
	var list []json.RawMessage
	if json.Unmarshal(raw, &list) == nil {
		parts := make([]string, 0, len(list))
		for _, m := range list {
			if t := markdownOf(m); t != "" {
				parts = append(parts, t)
			}
		}
		return strings.Join(parts, "\n\n")
	}
	var m struct {
		Kind     string `json:"kind"`
		Value    string `json:"value"`
		Language string `json:"language"`
	}
	if json.Unmarshal(raw, &m) != nil {
		return ""
	}
	if m.Language != "" {
		return "```" + m.Language + "\n" + m.Value + "\n```"
	}
	return m.Value
}

// completionKinds are LSP's CompletionItemKind names, by number.
var completionKinds = map[int]string{
	1: "text", 2: "method", 3: "function", 4: "constructor", 5: "field", 6: "variable", 7: "class",
	8: "interface", 9: "module", 10: "property", 11: "unit", 12: "value", 13: "enum", 14: "keyword",
	15: "snippet", 16: "color", 17: "file", 18: "reference", 19: "folder", 20: "enum-member",
	21: "constant", 22: "struct", 23: "event", 24: "operator", 25: "type-parameter",
}

type lspCompletionItem struct {
	Label      string          `json:"label"`
	Kind       int             `json:"kind"`
	Detail     string          `json:"detail"`
	InsertText string          `json:"insertText"`
	FilterText string          `json:"filterText"`
	SortText   string          `json:"sortText"`
	TextEdit   json.RawMessage `json:"textEdit"`
	// LabelDetails carries a signature or module for some servers.
	LabelDetails *struct {
		Detail      string `json:"detail"`
		Description string `json:"description"`
	} `json:"labelDetails"`
}

func (it lspCompletionItem) completion() Completion {
	c := Completion{Label: it.Label, Kind: completionKinds[it.Kind], Detail: it.Detail, Insert: it.Label, Filter: it.FilterText, Sort: it.SortText}
	if c.Detail == "" && it.LabelDetails != nil {
		c.Detail = strings.TrimSpace(it.LabelDetails.Detail + " " + it.LabelDetails.Description)
	}
	if it.InsertText != "" {
		c.Insert = it.InsertText
	}
	if len(it.TextEdit) > 0 && string(it.TextEdit) != "null" {
		// A TextEdit ({range, newText}) or an InsertReplaceEdit
		// ({insert, replace, newText}), whose insert range is used.
		var e struct {
			Range   *Range `json:"range"`
			Insert  *Range `json:"insert"`
			NewText string `json:"newText"`
		}
		if json.Unmarshal(it.TextEdit, &e) == nil {
			c.Insert = e.NewText
			if e.Range != nil {
				c.Range = e.Range
			} else {
				c.Range = e.Insert
			}
		}
	}
	return c
}

// ---- file URIs ----

// URI is path's file URI.
func URI(path string) string {
	p := filepath.ToSlash(path)
	if runtime.GOOS == "windows" && !strings.HasPrefix(p, "/") {
		p = "/" + p
	}
	return (&url.URL{Scheme: "file", Path: p}).String()
}

// PathOf is the absolute path of a file URI, "" for any other URI.
func PathOf(uri string) string {
	u, err := url.Parse(uri)
	if err != nil || u.Scheme != "file" {
		return ""
	}
	p := u.Path
	if runtime.GOOS == "windows" {
		p = strings.TrimPrefix(p, "/")
	}
	return filepath.FromSlash(p)
}
