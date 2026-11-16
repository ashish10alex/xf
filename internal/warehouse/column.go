package warehouse

import "encoding/json"

// Column is a column of a table or a query's result. Its Type is spelled as
// the warehouse spells it (INT64, BIGINT, STRUCT<a: INT>): impact compares
// types within one warehouse, so nothing maps one's types to another's.
type Column struct {
	Name string
	Type string
	// Fields are a STRUCT (RECORD) column's own columns.
	Fields []Column
	// Repeated says the column holds a list of Type (BigQuery's REPEATED
	// mode); Required that it is never NULL.
	Repeated, Required bool
	Description        string
	// RangeElement is the type of a BigQuery RANGE column's bounds (DATE,
	// DATETIME or TIMESTAMP), "" for any other column.
	RangeElement string

	// mode is the mode as the JSON the column was read from spelled it,
	// so writing it back sends the same bytes (BigQuery sends NULLABLE
	// or nothing for a nullable column).
	mode string
}

// Modes of a column, as BigQuery spells them.
const (
	ModeNullable = "NULLABLE"
	ModeRepeated = "REPEATED"
	ModeRequired = "REQUIRED"
)

// columnJSON is the shape a Column has on xf serve's wire and on xf web's
// page: BigQuery's schema field.
type columnJSON struct {
	Name             string        `json:"name"`
	Type             string        `json:"type"`
	Mode             string        `json:"mode"`
	Description      string        `json:"description"`
	Fields           []Column      `json:"fields"`
	RangeElementType *rangeElement `json:"rangeElementType,omitempty"`
}

type rangeElement struct {
	Type string `json:"type"`
}

// Mode is the column's mode as BigQuery spells it: REPEATED, REQUIRED, and
// for a nullable column NULLABLE or "", as the column was read.
func (c Column) Mode() string {
	switch {
	case c.Repeated:
		return ModeRepeated
	case c.Required:
		return ModeRequired
	case c.mode != ModeRepeated && c.mode != ModeRequired:
		return c.mode
	}
	return ""
}

// MarshalJSON writes the column in BigQuery's schema field shape.
func (c Column) MarshalJSON() ([]byte, error) {
	j := columnJSON{Name: c.Name, Type: c.Type, Mode: c.Mode(), Description: c.Description, Fields: c.Fields}
	if c.RangeElement != "" {
		j.RangeElementType = &rangeElement{Type: c.RangeElement}
	}
	return json.Marshal(j)
}

// UnmarshalJSON reads what MarshalJSON writes, which is BigQuery's own
// schema field.
func (c *Column) UnmarshalJSON(b []byte) error {
	var j columnJSON
	if err := json.Unmarshal(b, &j); err != nil {
		return err
	}
	*c = Column{Name: j.Name, Type: j.Type, Fields: j.Fields, Description: j.Description,
		Repeated: j.Mode == ModeRepeated, Required: j.Mode == ModeRequired, mode: j.Mode}
	if j.RangeElementType != nil {
		c.RangeElement = j.RangeElementType.Type
	}
	return nil
}
