package impact

import "encoding/json"

// JSONVersion is the version of the JSON document: fields are added under
// it, and it changes when one is removed or changes meaning.
const JSONVersion = 1

// severityNames are the severities as --fail-on and the JSON name them.
var severityNames = map[Severity]string{SeverityLow: "low", SeverityUnknown: "unknown", SeverityWarning: "warning", SeverityCritical: "critical"}

type jsonReport struct {
	Version int `json:"version"`
	// Worst is the most urgent severity of a change (see Worst), "" when
	// nothing is at risk.
	Worst string `json:"worst,omitempty"`
	Report
	AtRisk []jsonTable `json:"atRisk"`
}

// jsonTable is a table at risk with its severity (see TableSeverity), as
// the report's ledger marks it.
type jsonTable struct {
	Table
	Severity string       `json:"severity"`
	Columns  []jsonColumn `json:"columns"`
}

// jsonColumn is a dropped or retyped column with its severity (see
// ImpactSeverity).
type jsonColumn struct {
	Column
	Severity string `json:"severity"`
}

// JSON is the report for scripts: a document of JSONVersion with the
// report's fields, each table at risk and its columns with a severity, and
// its lists empty rather than null.
func JSON(r *Report) ([]byte, error) {
	out := jsonReport{Version: JSONVersion, Report: *r, AtRisk: []jsonTable{}}
	if worst, found := Worst(r); found {
		out.Worst = severityNames[worst]
	}
	for _, t := range r.AtRisk {
		jt := jsonTable{Table: t, Severity: severityNames[TableSeverity(t)], Columns: []jsonColumn{}}
		for _, c := range t.Columns {
			if c.Readers == nil {
				c.Readers = []Reader{}
			}
			jt.Columns = append(jt.Columns, jsonColumn{Column: c, Severity: severityNames[ImpactSeverity(c.Change, c.Readers, c.ReadersError)]})
		}
		out.AtRisk = append(out.AtRisk, jt)
	}
	for _, l := range []*[]Table{&out.Safe, &out.New} {
		if *l == nil {
			*l = []Table{}
		}
	}
	if out.Unchecked == nil {
		out.Unchecked = []Unchecked{}
	}
	data, err := json.MarshalIndent(out, "", "  ")
	if err != nil {
		return nil, err
	}
	return append(data, '\n'), nil
}
