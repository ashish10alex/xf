package impact

import (
	"strings"

	"github.com/ashish10alex/xf/internal/lineage"
	"github.com/ashish10alex/xf/internal/project"
)

const maxStaticDAGHops = 6

// StaticDAGReaders walks g's compiled dependency graph downstream of table to
// find project models that read column even when warehouse lineage (Dataplex,
// Unity Catalog, Snowflake) has not recorded them yet, and propagates column
// reads across multi-hop SELECT * views/models.
//
// Rules (designed for high precision):
//   - At hop 1 (direct dependents):
//     1. Any table/view/incremental model whose OUTERMOST SELECT list projects
//   - or alias.* (ignoring dbt import CTEs, COUNT(*), and arithmetic a * b):
//   - If EXCEPT(column) / EXCLUDE(column) does NOT exclude column, emit
//     ExactCopy and continue walking downstream.
//   - If EXCEPT(column) / EXCLUDE(column) DOES exclude column, emit Other
//     (BigQuery/Snowflake fail EXCEPT/EXCLUDE on a dropped column) and do
//     NOT propagate downstream.
//     2. Any view whose SQL explicitly mentions column emits Other (views don't
//     run materialization jobs, so Dataplex often has no links for them).
//   - At hops 2..maxStaticDAGHops (downstream of a SELECT * pass-through):
//     1. Another outermost SELECT * (not excluding column) emits ExactCopy and
//     keeps walking.
//     2. Any model whose SQL explicitly mentions column emits Other.
func StaticDAGReaders(g *project.Graph, table, column string) []lineage.ColumnLink {
	if g == nil || table == "" || column == "" {
		return nil
	}
	type hop struct {
		id    project.ActionID
		depth int
	}
	queue := []hop{{id: project.ActionID(table), depth: 0}}
	visited := map[project.ActionID]bool{project.ActionID(table): true}
	emitted := map[string]bool{}
	var out []lineage.ColumnLink

	leaf := column
	if dot := strings.LastIndexByte(column, '.'); dot >= 0 {
		leaf = column[dot+1:]
	}

	for len(queue) > 0 {
		cur := queue[0]
		queue = queue[1:]
		if cur.depth >= maxStaticDAGHops {
			continue
		}
		for _, dep := range g.Dependents(cur.id) {
			if dep == nil || visited[dep.ID] {
				continue
			}
			if dep.Kind == project.KindAssertion || dep.Kind == project.KindTest ||
				dep.Kind == project.KindOperation || dep.Kind == project.KindPropertyGraph ||
				dep.Kind == project.KindDeclaration || dep.Kind == project.KindNotebook {
				continue
			}
			sql := actionSQL(dep)
			if strings.TrimSpace(sql) == "" {
				continue
			}
			propagates, excluded := outerSelectStar(sql, leaf)
			if propagates {
				visited[dep.ID] = true
				if !emitted[string(dep.ID)] {
					emitted[string(dep.ID)] = true
					out = append(out, lineage.ColumnLink{
						Table:          string(dep.ID),
						Column:         column,
						DependencyType: lineage.ExactCopy,
					})
				}
				queue = append(queue, hop{id: dep.ID, depth: cur.depth + 1})
				continue
			}
			if excluded {
				visited[dep.ID] = true
				if !emitted[string(dep.ID)] {
					emitted[string(dep.ID)] = true
					out = append(out, lineage.ColumnLink{
						Table:          string(dep.ID),
						Column:         "",
						DependencyType: lineage.Other,
					})
				}
				continue
			}
			if (cur.depth > 0 || dep.Kind == project.KindView) && (Mentions(sql, column) || (leaf != column && Mentions(sql, leaf))) {
				visited[dep.ID] = true
				if !emitted[string(dep.ID)] {
					emitted[string(dep.ID)] = true
					out = append(out, lineage.ColumnLink{
						Table:          string(dep.ID),
						Column:         "",
						DependencyType: lineage.Other,
					})
				}
			}
		}
	}
	return out
}

type dagToken struct {
	text  string
	depth int
	start int
	end   int
}

func tokenizeDAGSQL(sql string) []dagToken {
	var toks []dagToken
	depth := 0
	for i := 0; i < len(sql); {
		c := sql[i]
		switch {
		case c == ' ' || c == '\t' || c == '\n' || c == '\r':
			i++
		case c == '(':
			toks = append(toks, dagToken{text: "(", depth: depth, start: i, end: i + 1})
			depth++
			i++
		case c == ')':
			if depth > 0 {
				depth--
			}
			toks = append(toks, dagToken{text: ")", depth: depth, start: i, end: i + 1})
			i++
		case c == '*' || c == ',' || c == '.' || c == ';':
			toks = append(toks, dagToken{text: sql[i : i+1], depth: depth, start: i, end: i + 1})
			i++
		case c == '`':
			j := i + 1
			for j < len(sql) && sql[j] != '`' {
				j++
			}
			end := min(len(sql), j+1)
			toks = append(toks, dagToken{text: strings.ToLower(sql[i+1 : min(j, len(sql))]), depth: depth, start: i, end: end})
			i = end
		case isIdentChar(c):
			j := i + 1
			for j < len(sql) && isIdentChar(sql[j]) {
				j++
			}
			toks = append(toks, dagToken{text: strings.ToLower(sql[i:j]), depth: depth, start: i, end: j})
			i = j
		default:
			i++
		}
	}
	return toks
}

var selectListEndKeywords = map[string]bool{
	"from": true, "where": true, "group": true, "order": true, "limit": true,
	"union": true, "intersect": true, "except": true, "qualify": true, "having": true,
	"window": true, ";": true,
}

// outerSelectStar inspects ONLY top-level (depth-0) SELECT projection lists
// (skipping CTE bodies in WITH (...) and subqueries). For each comma-separated
// projection item, a wildcard matches only `*` or `<ident>.*` (never arithmetic
// like `a * b` or `COUNT(*)`). Returns:
//   - propagates: true if any outermost wildcard projects column without
//     excluding it via EXCEPT(...) or EXCLUDE(...).
//   - excluded: true if an outermost wildcard explicitly names column inside
//     EXCEPT(...) or EXCLUDE(...) (so dropping column breaks the query, but
//     column is not propagated downstream).
func outerSelectStar(sql, column string) (propagates, excluded bool) {
	stripped := project.StripSQL(sql, project.StripOptions{BlankSingleStrings: true, BlankDoubleStrings: true})
	toks := tokenizeDAGSQL(stripped)
	for i := 0; i < len(toks); i++ {
		if toks[i].depth != 0 || toks[i].text != "select" {
			continue
		}
		j := i + 1
		if j < len(toks) && toks[j].depth == 0 && (toks[j].text == "distinct" || toks[j].text == "all") {
			j++
		}
		// Walk comma-separated projection items at depth 0 until FROM/WHERE/etc.
		itemStart := j
		for k := j; k <= len(toks); k++ {
			atEnd := k == len(toks)
			isSep := false
			isClauseEnd := false
			if !atEnd && toks[k].depth == 0 {
				if toks[k].text == "," {
					isSep = true
				} else if selectListEndKeywords[toks[k].text] {
					// EXCEPT right after * is SELECT * EXCEPT (...), not set-op EXCEPT.
					if toks[k].text == "except" && k > itemStart && toks[k-1].text == "*" {
						continue
					}
					isClauseEnd = true
				}
			}
			if atEnd || isSep || isClauseEnd {
				p, ex := inspectProjectionItem(toks[itemStart:k], stripped, column)
				if p {
					propagates = true
				}
				if ex {
					excluded = true
				}
				if atEnd || isClauseEnd {
					i = k
					break
				}
				itemStart = k + 1
			}
		}
	}
	if propagates {
		return true, false
	}
	return false, excluded
}

func inspectProjectionItem(item []dagToken, stripped, column string) (propagates, excluded bool) {
	if len(item) == 0 {
		return false, false
	}
	// Wildcard projection item must start with `*` or `<alias>.*` (or `<a>.<b>.*`).
	starIdx := -1
	if item[0].text == "*" && item[0].depth == 0 {
		starIdx = 0
	} else {
		// Check <ident> (`.` <ident>)* `.` `*`
		k := 0
		for k+2 < len(item) && item[k].depth == 0 && item[k+1].text == "." && item[k+1].depth == 0 {
			if item[k+2].text == "*" && item[k+2].depth == 0 {
				starIdx = k + 2
				break
			}
			k += 2
		}
	}
	if starIdx < 0 {
		return false, false
	}
	// Must not be followed by an arithmetic expression before EXCEPT/EXCLUDE/REPLACE.
	// Check for EXCEPT (...) or EXCLUDE (...) immediately after `*`.
	for k := starIdx + 1; k < len(item); k++ {
		if item[k].depth != 0 {
			continue
		}
		if item[k].text == "except" || item[k].text == "exclude" {
			// Find the matching parenthesized list (or bare column for Snowflake EXCLUDE col).
			if k+1 < len(item) && item[k+1].text == "(" {
				pStart := item[k+1].end
				pEnd := len(stripped)
				for m := k + 2; m < len(item); m++ {
					if item[m].text == ")" && item[m].depth == 0 {
						pEnd = item[m].start
						break
					}
				}
				if Mentions(stripped[pStart:pEnd], column) {
					return false, true
				}
			} else if k+1 < len(item) && strings.EqualFold(item[k+1].text, column) {
				return false, true
			}
		}
	}
	return true, false
}
