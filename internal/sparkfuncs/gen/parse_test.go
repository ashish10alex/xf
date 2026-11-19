package main

import (
	"reflect"
	"testing"
)

func TestParseParams(t *testing.T) {
	for _, c := range []struct {
		in   string
		want []Param
	}{
		{"str, pos[, len]", []Param{{Name: "str"}, {Name: "pos"}, {Name: "len", Optional: true}}},
		{"input[, offset[, default]]", []Param{{Name: "input"}, {Name: "offset", Optional: true}, {Name: "default", Optional: true}}},
		{"str FROM pos[ FOR len]]", []Param{{Name: "str"}, {Name: "pos", Before: "FROM"}, {Name: "len", Before: "FOR", Optional: true}}},
		{"expr1, expr2, ...", []Param{{Name: "expr1"}, {Name: "expr2", Repeated: true}}},
		{"expr[, expr...]", []Param{{Name: "expr", Repeated: true}}},
		{"sep[, str | array(str)]+", []Param{{Name: "sep"}, {Name: "str | array(str)", Optional: true, Repeated: true}}},
		{"class, method[, arg1[, arg2 ..]]", []Param{{Name: "class"}, {Name: "method"}, {Name: "arg1", Optional: true}, {Name: "arg2", Optional: true, Repeated: true}}},
		{"DISTINCT expr[, expr...]", []Param{{Name: "DISTINCT expr", Repeated: true}}},
		{"col, percentage [, accuracy]", []Param{{Name: "col"}, {Name: "percentage"}, {Name: "accuracy", Optional: true}}},
		{"expr AS type", []Param{{Name: "expr"}, {Name: "type", Before: "AS"}}},
		{"[DISTINCT] expr", []Param{{Name: "DISTINCT", Optional: true}, {Name: "expr"}}},
		{"array(1, 2), x", []Param{{Name: "array(1, 2)"}, {Name: "x"}}},
		{"", []Param{}},
	} {
		if got := parseParams(c.in); !reflect.DeepEqual(got, c.want) {
			t.Errorf("parseParams(%q) = %+v, want %+v", c.in, got, c.want)
		}
	}
}

const page = `<h3 id="date_trunc"><a>date_trunc</a></h3>
<p>date_trunc(fmt, ts) - Returns timestamp <code>ts</code> truncated to the unit specified by the format model <code>fmt</code>.</p>
<p><strong>Arguments:</strong></p>
<ul>
<li>fmt - the format representing the unit to be truncated to<ul>
<li>"YEAR", "YYYY", "YY" - truncate to the year</li>
<li>"MONTH", "MM", "MON" - truncate to the month</li>
</ul>
</li>
<li>ts - datetime value or valid timestamp string
  An expression that evaluates to a timestamp.</li>
</ul>
<p><strong>Examples:</strong></p>
<pre><code>&gt; SELECT date_trunc('YEAR', '2015-03-05T09:32:05.359');
 2015-01-01 00:00:00
</code></pre>
<p><strong>Since:</strong> 2.3.0</p>
<h3 id="||"><a>||</a></h3>
<p>expr1 || expr2 - Returns the concatenation of <code>expr1</code> and <code>expr2</code>.</p>
<h3 id="current_date"><a>current_date</a></h3>
<p>current_date() - Returns the current date.</p>
<p>current_date - Returns the current date.</p>
<h3 id="percentile_cont"><a>percentile_cont</a></h3>
<p>percentile_cont(percentage) WITHIN GROUP (ORDER BY col) - Returns a percentile.</p>
<p><strong>Examples:</strong></p>
<pre><code>&gt; SELECT percentile_cont(0.25) WITHIN GROUP (ORDER BY col) FROM VALUES (0), (10) AS tab(col);
 2.5
</code></pre>
`

func TestParsePage(t *testing.T) {
	fs := parsePage(page, "agg-functions")
	if len(fs) != 3 {
		t.Fatalf("got %d functions, want 3 (|| is no call): %+v", len(fs), fs)
	}
	dt := fs[0]
	if dt.Name != "DATE_TRUNC" || dt.Doc != "Returns timestamp `ts` truncated to the unit specified by the format model `fmt`." {
		t.Errorf("date_trunc = %q: %q", dt.Name, dt.Doc)
	}
	fmt := dt.Signatures[0].Params[0]
	if !reflect.DeepEqual(fmt.Values, []string{"'YEAR'", "'MONTH'"}) {
		t.Errorf("fmt's values = %q, want the first of each, quoted as the examples quote them", fmt.Values)
	}
	if ts := dt.Signatures[0].Params[1]; ts.Doc != "datetime value or valid timestamp string. An expression that evaluates to a timestamp" {
		t.Errorf("ts's doc = %q", ts.Doc)
	}
	if dt.Example == nil || dt.Example.SQL != "date_trunc('YEAR', '2015-03-05T09:32:05.359')" || dt.Example.Result != "2015-01-01 00:00:00" {
		t.Errorf("example = %+v", dt.Example)
	}
	if !dt.Aggregate || dt.Window != "optional" {
		t.Errorf("an agg-functions page's function is an aggregate that takes OVER: %+v", dt)
	}
	if cd := fs[1]; len(cd.Signatures) != 1 || len(cd.Signatures[0].Params) != 0 {
		t.Errorf("current_date() and current_date are one signature: %+v", cd.Signatures)
	}
	pc := fs[2]
	if !reflect.DeepEqual(pc.Notes, []string{"Followed by `WITHIN GROUP (ORDER BY col)`."}) {
		t.Errorf("percentile_cont's notes = %q", pc.Notes)
	}
	if pc.Example == nil || pc.Example.SQL != "SELECT percentile_cont(0.25) WITHIN GROUP (ORDER BY col) FROM VALUES (0), (10) AS tab(col)" {
		t.Errorf("an example with FROM keeps its SELECT: %+v", pc.Example)
	}
}

func TestQuoteValues(t *testing.T) {
	if got := quoteValues([]string{"YEAR", "MONTH"}, "&gt; SELECT extract(YEAR FROM ts);"); !reflect.DeepEqual(got, []string{"YEAR", "MONTH"}) {
		t.Errorf("EXTRACT's fields stay bare: %q", got)
	}
}
