package dataform

import (
	"slices"
	"testing"
)

func TestSplitOptions(t *testing.T) {
	for _, c := range []struct {
		in   string
		want []string
	}{
		{"", nil},
		{"--schema-suffix=ashish", []string{"--schema-suffix=ashish"}},
		{"  --schema-suffix=ashish   --vars=env=dev ", []string{"--schema-suffix=ashish", "--vars=env=dev"}},
		{`--vars="label=a b" --table-prefix='x y'`, []string{"--vars=label=a b", "--table-prefix=x y"}},
		{`--vars=""`, []string{"--vars="}},
	} {
		got, err := SplitOptions(c.in)
		if err != nil || !slices.Equal(got, c.want) {
			t.Errorf("SplitOptions(%q) = %q, %v", c.in, got, err)
		}
	}
	if _, err := SplitOptions(`--vars="a`); err == nil {
		t.Error("an unclosed quote is an error")
	}
}
