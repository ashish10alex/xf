// Package sparkfuncs is what xf knows of Databricks SQL's functions, for the
// completion, hover and signature help of xf web's editors on a Databricks
// project, as bqfuncs is BigQuery's.
//
// spark-functions.json is written by `just spark-functions` (./gen) from
// Apache Spark's built-in function reference (Apache License 2.0: see
// LICENSE), in bqfuncs' catalog format. Spark's reference gives no types, so
// a signature names its arguments alone. Each function links to its page in
// the Databricks SQL reference, or to Spark's where Databricks has none.
// extra.json and clauses.json are xf's own: Databricks' functions and forms
// Spark's reference lacks (TIMESTAMPADD, DATEDIFF(unit, …)), the values of
// arguments it lists none for, and PIVOT and UNPIVOT.
package sparkfuncs

import (
	_ "embed"
	"fmt"
	"slices"
	"strings"
	"sync"

	"github.com/ashish10alex/xf/internal/bqfuncs"
)

//go:embed spark-functions.json
var catalogJSON []byte

//go:embed extra.json
var extraJSON []byte

//go:embed clauses.json
var clausesJSON []byte

// All is every function, in name order: the catalog's, with extra.json's
// in place of or beside them, and the clauses.
var All = sync.OnceValues(load)

func load() ([]bqfuncs.Func, error) {
	out, err := bqfuncs.LoadCatalog(catalogJSON, nil)
	if err != nil {
		return nil, fmt.Errorf("spark-functions.json: %w", err)
	}
	extra, err := bqfuncs.LoadCatalog(extraJSON, nil)
	if err != nil {
		return nil, fmt.Errorf("extra.json: %w", err)
	}
	for _, e := range extra {
		if i := slices.IndexFunc(out, func(f bqfuncs.Func) bool { return f.Name == e.Name }); i >= 0 {
			e.Aggregate = out[i].Aggregate
			out[i] = e
		} else {
			out = append(out, e)
		}
	}
	clauses, err := bqfuncs.LoadCatalog(clausesJSON, nil)
	if err != nil {
		return nil, fmt.Errorf("clauses.json: %w", err)
	}
	for _, c := range clauses {
		c.Clause = true
		out = append(out, c)
	}
	slices.SortStableFunc(out, func(a, b bqfuncs.Func) int { return strings.Compare(a.Name, b.Name) })
	return out, nil
}
