package navigate

import (
	"bytes"
	"regexp"
)

// A dbt project's SQL is Jinja: {{ ref('orders') }}, {{ source('raw',
// 'orders') }}, {{ cents_to_dollars('amount') }}, {% macro name(args) %}.
// This file finds those tags and the calls in them without a Jinja parser:
// enough to navigate, not to render.

// jinjaTag is the inside of a {{ }} or {% %} tag; {# #} comments are none.
type jinjaTag struct {
	// Start and End bound the text between the delimiters, whitespace
	// control dashes ({{- -}}) included. End is len(src) for a tag that
	// isn't closed.
	Start, End int
	// Expr is set for {{ }}, unset for {% %}.
	Expr bool
}

// jinjaTags returns every tag in src, in order. Jinja renders tags in SQL
// comments too, so they count.
func jinjaTags(src []byte) []jinjaTag {
	var out []jinjaTag
	for i := 0; i+1 < len(src); {
		if src[i] != '{' {
			i++
			continue
		}
		var closer string
		switch src[i+1] {
		case '{':
			closer = "}}"
		case '%':
			closer = "%}"
		case '#':
			end := indexFrom(src, "#}", i+2)
			if end < 0 {
				return out
			}
			i = end + 2
			continue
		default:
			i++
			continue
		}
		start := i + 2
		end := tagEnd(src, start, closer)
		out = append(out, jinjaTag{Start: start, End: end, Expr: closer == "}}"})
		i = end + 2
	}
	return out
}

// tagEnd returns where closer ends the tag whose text starts at i, skipping
// quoted strings; len(src) when nothing does.
func tagEnd(src []byte, i int, closer string) int {
	for i < len(src) {
		switch c := src[i]; {
		case c == '\'' || c == '"':
			i, _ = jinjaStringEnd(src, i)
		case bytes.HasPrefix(src[i:], []byte(closer)):
			return i
		default:
			i++
		}
	}
	return len(src)
}

// jinjaStringEnd returns the offset just past the string literal starting
// at i, and whether its quote closes it. One that doesn't ends at the end
// of its line.
func jinjaStringEnd(src []byte, i int) (int, bool) {
	q := src[i]
	for j := i + 1; j < len(src); j++ {
		switch src[j] {
		case '\\':
			j++
		case '\n':
			return j, false
		case q:
			return j + 1, true
		}
	}
	return len(src), false
}

// openTag returns the tag offset is in when it isn't closed before offset,
// as while one is being typed: {{ ref('or|. Only the text before offset
// counts, so a tag closed later on is found too.
func openTag(src []byte, offset int) (jinjaTag, bool) {
	for _, t := range jinjaTags(src[:offset]) {
		if t.End == offset {
			return t, true
		}
	}
	return jinjaTag{}, false
}

// jinjaCall is a call in a tag: ref('orders'), dbt_utils.star(from=x).
type jinjaCall struct {
	// Func is the name called, with its namespace: "ref", "dbt_utils.star".
	Func string
	// Start is where Func starts and End just past the closing paren, or
	// the end of the tag for a call not closed yet.
	Start, End int
	Args       []jinjaArg
	// Closed is set when the call's ) is in the tag.
	Closed bool
}

// jinjaArg is one argument of a call.
type jinjaArg struct {
	// Key is a keyword argument's name; empty for a positional one.
	Key string
	// Value is a string literal's contents or a number's text; Literal is
	// unset for anything else, a variable or call, and then Value is empty.
	Value   string
	Literal bool
	// Start and End bound Value: inside the quotes of a string.
	Start, End int
	// Open is set for a string still being typed: its quote isn't closed.
	Open bool
}

// positional returns the call's positional arguments.
func (c jinjaCall) positional() []jinjaArg {
	var out []jinjaArg
	for _, a := range c.Args {
		if a.Key == "" {
			out = append(out, a)
		}
	}
	return out
}

// keyword returns the keyword argument called one of keys.
func (c jinjaCall) keyword(keys ...string) (jinjaArg, bool) {
	for _, a := range c.Args {
		for _, k := range keys {
			if a.Key == k {
				return a, true
			}
		}
	}
	return jinjaArg{}, false
}

type jtokKind int

const (
	jtokIdent jtokKind = iota // a name, dotted: dbt_utils.star
	jtokString
	jtokNumber
	jtokOpen
	jtokClose
	jtokComma
	jtokAssign
	jtokOther
)

type jtok struct {
	kind       jtokKind
	start, end int
	// open is set on a string whose quote isn't closed.
	open bool
}

// jinjaTokens tokenizes src[start:end].
func jinjaTokens(src []byte, start, end int) []jtok {
	var out []jtok
	for i := start; i < end; {
		c := src[i]
		switch {
		case isSpace(c):
			i++
		case c == '\'' || c == '"':
			j, closed := jinjaStringEnd(src[:end], i)
			out = append(out, jtok{kind: jtokString, start: i, end: j, open: !closed})
			i = j
		case isIdentStart(c):
			j := i + 1
			for j < end && (isWordByte(src[j]) || src[j] == '.' && j+1 < end && isIdentStart(src[j+1])) {
				j++
			}
			out = append(out, jtok{kind: jtokIdent, start: i, end: j})
			i = j
		case c >= '0' && c <= '9':
			j := i + 1
			for j < end && (isWordByte(src[j]) || src[j] == '.') {
				j++
			}
			out = append(out, jtok{kind: jtokNumber, start: i, end: j})
			i = j
		default:
			kind := jtokOther
			switch c {
			case '(':
				kind = jtokOpen
			case ')':
				kind = jtokClose
			case ',':
				kind = jtokComma
			case '=':
				if i+1 < end && src[i+1] == '=' {
					out = append(out, jtok{kind: jtokOther, start: i, end: i + 2})
					i += 2
					continue
				}
				kind = jtokAssign
			}
			out = append(out, jtok{kind: kind, start: i, end: i + 1})
			i++
		}
	}
	return out
}

// jinjaCalls returns the calls in src[start:end], outer ones before the
// calls in their arguments.
func jinjaCalls(src []byte, start, end int) []jinjaCall {
	toks := jinjaTokens(src, start, end)
	type frame struct {
		call int // index in out; -1 for a bare ( group
		arg  []jtok
	}
	var out []jinjaCall
	var stack []frame
	finishArg := func(f *frame) {
		if f.call < 0 || len(f.arg) == 0 {
			f.arg = nil
			return
		}
		c := &out[f.call]
		a := jinjaArg{}
		toks := f.arg
		if len(toks) >= 2 && toks[0].kind == jtokIdent && toks[1].kind == jtokAssign {
			a.Key = string(src[toks[0].start:toks[0].end])
			toks = toks[2:]
		}
		if len(toks) == 1 && (toks[0].kind == jtokString || toks[0].kind == jtokNumber) {
			t := toks[0]
			a.Literal, a.Start, a.End, a.Open = true, t.start, t.end, t.open
			if t.kind == jtokString {
				a.Start++
				if !t.open {
					a.End--
				}
			}
			a.Value = string(src[a.Start:a.End])
		}
		c.Args = append(c.Args, a)
		f.arg = nil
	}
	for i, t := range toks {
		switch t.kind {
		case jtokOpen:
			if len(stack) > 0 {
				stack[len(stack)-1].arg = append(stack[len(stack)-1].arg, t)
			}
			if i > 0 && toks[i-1].kind == jtokIdent && onlySpace(src[toks[i-1].end:t.start]) {
				prev := toks[i-1]
				out = append(out, jinjaCall{Func: string(src[prev.start:prev.end]), Start: prev.start, End: end})
				stack = append(stack, frame{call: len(out) - 1})
			} else {
				stack = append(stack, frame{call: -1})
			}
		case jtokClose:
			if len(stack) == 0 {
				continue
			}
			f := &stack[len(stack)-1]
			finishArg(f)
			if f.call >= 0 {
				out[f.call].End, out[f.call].Closed = t.end, true
			}
			stack = stack[:len(stack)-1]
			if len(stack) > 0 {
				stack[len(stack)-1].arg = append(stack[len(stack)-1].arg, t)
			}
		case jtokComma:
			if len(stack) > 0 {
				finishArg(&stack[len(stack)-1])
			}
		default:
			if len(stack) > 0 {
				stack[len(stack)-1].arg = append(stack[len(stack)-1].arg, t)
			}
		}
	}
	for i := len(stack) - 1; i >= 0; i-- {
		finishArg(&stack[i])
	}
	return out
}

func onlySpace(b []byte) bool {
	for _, c := range b {
		if !isSpace(c) {
			return false
		}
	}
	return true
}

// callAt returns the innermost call in a tag of src whose name or
// arguments hold offset.
func callAt(src []byte, offset int) (jinjaCall, bool) {
	for _, t := range jinjaTags(src) {
		if offset < t.Start || offset > t.End {
			continue
		}
		var best jinjaCall
		found := false
		for _, c := range jinjaCalls(src, t.Start, t.End) {
			if offset >= c.Start && offset <= c.End && (!found || c.Start >= best.Start) {
				best, found = c, true
			}
		}
		return best, found
	}
	return jinjaCall{}, false
}

// macroDef matches the tag that opens a macro or generic test definition:
// {% macro name(args) %}, {%- test name(model, column_name) -%}.
var macroDef = regexp.MustCompile(`^-?\s*(macro|test)\s+(\w+)\s*\(`)

// macroBlock is a macro or test definition in a file.
type macroBlock struct {
	// Keyword is "macro" or "test"; Name is as written, without test_.
	Keyword, Name string
	// Start is the opening {%, End just past the closing tag's %} (the end
	// of the file when there is none).
	Start, End int
	// NameStart and NameEnd bound the name.
	NameStart, NameEnd int
	// Signature is the definition as a call: name(args).
	Signature string
}

// macroBlocks returns the macro and test definitions in src, in order.
func macroBlocks(src []byte) []macroBlock {
	tags := jinjaTags(src)
	var out []macroBlock
	for i, t := range tags {
		if t.Expr {
			continue
		}
		m := macroDef.FindSubmatchIndex(src[t.Start:t.End])
		if m == nil {
			continue
		}
		b := macroBlock{
			Keyword: string(src[t.Start+m[2] : t.Start+m[3]]),
			Name:    string(src[t.Start+m[4] : t.Start+m[5]]),
			Start:   t.Start - 2, End: len(src),
			NameStart: t.Start + m[4], NameEnd: t.Start + m[5],
		}
		sig := bytes.TrimRight(src[b.NameStart:t.End], " \t\r\n-")
		b.Signature = compactCall(sig)
		for _, u := range tags[i+1:] {
			if u.Expr {
				continue
			}
			body := bytes.Trim(src[u.Start:u.End], " \t\r\n-")
			if string(body) == "end"+b.Keyword {
				b.End = min(u.End+2, len(src))
				break
			}
		}
		out = append(out, b)
	}
	return out
}
