package dataform

import (
	"encoding/json"
	"errors"
	"fmt"
)

// compileResultKeys are top-level keys that identify a compile result. The
// output always has at least one of them, even when the arrays are empty.
var compileResultKeys = []string{"tables", "operations", "assertions", "declarations", "graphErrors", "projectConfig"}

func looksLikeCompileResult(raw []byte) bool {
	var obj map[string]json.RawMessage
	if err := json.Unmarshal(raw, &obj); err != nil {
		return false
	}
	for _, k := range compileResultKeys {
		if _, ok := obj[k]; ok {
			return true
		}
	}
	return false
}

// topLevelObjects returns each balanced top-level {...} object in s, ignoring
// braces inside JSON string literals and stray closing braces.
func topLevelObjects(s []byte) [][]byte {
	var out [][]byte
	depth, start := 0, -1
	inString, escape := false, false
	for i, c := range s {
		if inString {
			switch {
			case escape:
				escape = false
			case c == '\\':
				escape = true
			case c == '"':
				inString = false
			}
			continue
		}
		switch c {
		case '"':
			inString = true
		case '{':
			if depth == 0 {
				start = i
			}
			depth++
		case '}':
			// A stray closer must not push depth negative and desync the next
			// object's start position.
			if depth == 0 {
				continue
			}
			depth--
			if depth == 0 && start != -1 {
				out = append(out, s[start:i+1])
				start = -1
			}
		}
	}
	return out
}

// ExtractCompileJSON returns the compile result from `dataform compile --json`
// stdout. Older Dataform versions (e.g. 2.9) print a logger JSON object before
// the result, so stdout can hold several JSON values back to back; the last one
// that looks like a compile result wins.
func ExtractCompileJSON(stdout []byte) ([]byte, error) {
	if json.Valid(stdout) && looksLikeCompileResult(stdout) {
		return stdout, nil
	}
	var last []byte
	for _, chunk := range topLevelObjects(stdout) {
		if looksLikeCompileResult(chunk) {
			last = chunk
		}
	}
	if last != nil {
		return last, nil
	}
	var v any
	if err := json.Unmarshal(stdout, &v); err != nil {
		return nil, fmt.Errorf("no compile result in output: %w", err)
	}
	return nil, errors.New("no compile result in output")
}
