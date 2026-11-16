package warehouse

import (
	"encoding/json"
	"testing"
)

// A schema read from BigQuery is written back byte for byte: xf serve's
// wire and xf web's page carry columns in BigQuery's own shape.
func TestColumnJSONRoundTrip(t *testing.T) {
	const schema = `[{"name":"a","type":"INTEGER","mode":"NULLABLE","description":"","fields":null},` +
		`{"name":"b","type":"STRING","mode":"","description":"the b","fields":null},` +
		`{"name":"c","type":"STRING","mode":"REPEATED","description":"","fields":null},` +
		`{"name":"d","type":"INTEGER","mode":"REQUIRED","description":"","fields":null},` +
		`{"name":"r","type":"RECORD","mode":"REPEATED","description":"","fields":[{"name":"x","type":"TIMESTAMP","mode":"NULLABLE","description":"","fields":null}]},` +
		`{"name":"g","type":"RANGE","mode":"","description":"","fields":null,"rangeElementType":{"type":"DATE"}}]`
	var cols []Column
	if err := json.Unmarshal([]byte(schema), &cols); err != nil {
		t.Fatal(err)
	}
	if !cols[2].Repeated || !cols[3].Required || cols[0].Repeated || cols[0].Required || !cols[4].Repeated || cols[5].RangeElement != "DATE" || cols[1].Description != "the b" {
		t.Errorf("decoded %+v", cols)
	}
	if got, _ := json.Marshal(cols); string(got) != schema {
		t.Errorf("wrote\n%s\nwant\n%s", got, schema)
	}
}

// A column made in Go, not read, says its mode from Repeated and Required.
func TestColumnMode(t *testing.T) {
	for c, want := range map[*Column]string{
		{Name: "a"}:                 "",
		{Name: "a", Repeated: true}: ModeRepeated,
		{Name: "a", Required: true}: ModeRequired,
	} {
		if got := c.Mode(); got != want {
			t.Errorf("%+v: Mode = %q, want %q", *c, got, want)
		}
	}
	b, _ := json.Marshal(Column{Name: "a", Type: "INT64", Repeated: true})
	if want := `{"name":"a","type":"INT64","mode":"REPEATED","description":"","fields":null}`; string(b) != want {
		t.Errorf("wrote %s, want %s", b, want)
	}
}
