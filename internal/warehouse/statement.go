package warehouse

import (
	"strings"
	"unicode"
)

// readKeywords start a statement that changes nothing: its type is SELECT.
var readKeywords = map[string]bool{
	"SELECT": true, "WITH": true, "VALUES": true, "TABLE": true, "FROM": true,
	"SHOW": true, "DESCRIBE": true, "DESC": true, "EXPLAIN": true, "LIST": true,
}

// objectSkip are the words between CREATE, ALTER or DROP and what it acts
// on: CREATE OR REPLACE TEMPORARY VIEW is CREATE_VIEW.
var objectSkip = map[string]bool{
	"OR": true, "REPLACE": true, "TEMP": true, "TEMPORARY": true, "GLOBAL": true,
	"IF": true, "NOT": true, "EXISTS": true, "EXTERNAL": true, "STREAMING": true,
}

// StatementType guesses a statement's type from its first words, as
// BigQuery names types (SELECT, INSERT, CREATE_TABLE, DROP_VIEW), for a
// warehouse with no dry run to say it. A statement that reads only is
// SELECT; one with no words is "".
func StatementType(sql string) string {
	words := leadingWords(sql, 6)
	if len(words) == 0 {
		return ""
	}
	first := words[0]
	switch {
	case readKeywords[first]:
		return "SELECT"
	case first == "CREATE" || first == "ALTER" || first == "DROP" || first == "REPLACE":
		if first == "REPLACE" { // REPLACE TABLE is Databricks' CREATE OR REPLACE
			first = "CREATE"
		}
		for _, w := range words[1:] {
			if !objectSkip[w] {
				if w == "MATERIALIZED" {
					w = "MATERIALIZED_VIEW"
				}
				return first + "_" + w
			}
		}
	}
	return first
}

// leadingWords are the first n words of sql in upper case, after its
// comments and any opening parentheses.
func leadingWords(sql string, n int) []string {
	var out []string
	s := sql
	for len(out) < n {
		s = strings.TrimLeftFunc(s, func(r rune) bool { return unicode.IsSpace(r) || r == '(' })
		switch {
		case s == "":
			return out
		case strings.HasPrefix(s, "--"):
			_, rest, ok := strings.Cut(s, "\n")
			if !ok {
				return out
			}
			s = rest
			continue
		case strings.HasPrefix(s, "/*"):
			_, rest, ok := strings.Cut(s[2:], "*/")
			if !ok {
				return out
			}
			s = rest
			continue
		}
		end := strings.IndexFunc(s, func(r rune) bool { return !unicode.IsLetter(r) && r != '_' })
		if end == 0 {
			return out
		}
		if end < 0 {
			end = len(s)
		}
		out = append(out, strings.ToUpper(s[:end]))
		s = s[end:]
	}
	return out
}
