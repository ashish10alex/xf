package cost

import (
	"context"
	"strings"
	"testing"

	"github.com/ashish10alex/xf/internal/changes"
	"github.com/ashish10alex/xf/internal/project"
	"github.com/ashish10alex/xf/internal/warehouse"
)

func TestPredicateAwareUnfilteredAndMergeTarget(t *testing.T) {
	parts := map[warehouse.TableRef]*warehouse.Partition{
		{Project: "acme", Dataset: "raw", Table: "events"}:   {Column: "event_date"},
		{Project: "acme", Dataset: "marts", Table: "orders"}: {Column: "order_date"},
	}
	ps := &partitions{
		lookup: func(_ context.Context, t warehouse.TableRef) (*warehouse.Partition, error) { return parts[t], nil },
		seen:   map[warehouse.TableRef]*partitionLookup{},
	}
	eventsRef := warehouse.TableRef{Project: "acme", Dataset: "raw", Table: "events"}
	ordersRef := warehouse.TableRef{Project: "acme", Dataset: "marts", Table: "orders"}

	// 1. Naming event_date only in SELECT / GROUP BY / ORDER BY / CASE WHEN is NOT a filter.
	selectOnly := ps.unfiltered(context.Background(),
		"SELECT event_date, CASE WHEN event_date > '2026-01-01' THEN 1 END FROM `acme.raw.events` GROUP BY event_date ORDER BY event_date",
		[]warehouse.TableRef{eventsRef})
	if len(selectOnly) != 1 || selectOnly[0].Column != "event_date" || selectOnly[0].MergeTarget {
		t.Fatalf("selectOnly = %+v, want unfiltered event_date", selectOnly)
	}

	// 2. Filtering in outer CTE WHERE counts as filtered (BigQuery pushes down).
	cteFiltered := ps.unfiltered(context.Background(),
		"WITH e AS (SELECT event_date, id FROM `acme.raw.events`) SELECT * FROM e WHERE event_date >= CURRENT_DATE()",
		[]warehouse.TableRef{eventsRef})
	if len(cteFiltered) != 0 {
		t.Fatalf("cteFiltered = %+v, want none", cteFiltered)
	}

	// 3. MERGE filtering source in USING subquery but NOT target in ON flags target with MergeTarget=true.
	mergeBad := ps.unfiltered(context.Background(), `
		MERGE `+"`acme.marts.orders`"+` T
		USING (SELECT id, order_date FROM `+"`acme.raw.events`"+` WHERE event_date >= CURRENT_DATE()) S
		ON T.id = S.id
		WHEN MATCHED THEN UPDATE SET T.order_date = S.order_date`,
		[]warehouse.TableRef{ordersRef, eventsRef})
	if len(mergeBad) != 1 || mergeBad[0].Table != "acme.marts.orders" || !mergeBad[0].MergeTarget {
		t.Fatalf("mergeBad = %+v, want MergeTarget warning on acme.marts.orders only", mergeBad)
	}

	// 4. MERGE filtering target partition column in ON is clean (even with a nested JOIN ON inside USING).
	mergeGood := ps.unfiltered(context.Background(), `
		MERGE `+"`acme.marts.orders`"+` T
		USING (SELECT a.id FROM `+"`acme.raw.events`"+` a JOIN other b ON a.id = b.id WHERE a.event_date >= CURRENT_DATE()) S
		ON T.id = S.id AND T.order_date >= CURRENT_DATE()
		WHEN MATCHED THEN DELETE`,
		[]warehouse.TableRef{ordersRef, eventsRef})
	if len(mergeGood) != 0 {
		t.Fatalf("mergeGood = %+v, want none", mergeGood)
	}

	// 5. dbt insert_overwrite MERGE filtering target in WHEN ... AND is clean.
	mergeInsertOverwrite := ps.unfiltered(context.Background(), `
		MERGE `+"`acme.marts.orders`"+` DEST
		USING (SELECT id, order_date FROM `+"`acme.raw.events`"+` WHERE event_date >= CURRENT_DATE()) SRC
		ON FALSE
		WHEN NOT MATCHED BY SOURCE AND DEST.order_date IN (CURRENT_DATE()) THEN DELETE
		WHEN NOT MATCHED THEN INSERT ROW`,
		[]warehouse.TableRef{ordersRef, eventsRef})
	if len(mergeInsertOverwrite) != 0 {
		t.Fatalf("mergeInsertOverwrite = %+v, want none", mergeInsertOverwrite)
	}

	// 6. Second MERGE into same table missing partition filter is caught; also
	// works even if referencedTables omitted the MERGE target.
	mergeTwo := ps.unfiltered(context.Background(), `
		MERGE `+"`acme.marts.orders`"+` T USING s ON T.id = s.id AND T.order_date = CURRENT_DATE() WHEN MATCHED THEN DELETE;
		MERGE `+"`acme.marts.orders`"+` T USING s ON T.id = s.id WHEN MATCHED THEN DELETE`,
		nil)
	if len(mergeTwo) != 1 || mergeTwo[0].Table != "acme.marts.orders" || !mergeTwo[0].MergeTarget {
		t.Fatalf("mergeTwo = %+v, want MergeTarget on acme.marts.orders", mergeTwo)
	}

	// 7. WHERE LEFT(event_date, 4) and WHERE EXTRACT(DATE FROM event_date) count as filtered;
	// HAVING / QUALIFY do not prune partitions.
	for _, sql := range []string{
		"SELECT id FROM `acme.raw.events` WHERE LEFT(event_date, 4) = '2026'",
		"SELECT id FROM `acme.raw.events` WHERE EXTRACT(YEAR FROM event_date) = 2026",
	} {
		if got := ps.unfiltered(context.Background(), sql, []warehouse.TableRef{eventsRef}); len(got) != 0 {
			t.Errorf("%q: got %+v, want filtered", sql, got)
		}
	}
	for _, sql := range []string{
		"SELECT event_date, COUNT(*) FROM `acme.raw.events` GROUP BY 1 HAVING event_date >= CURRENT_DATE()",
		"SELECT id FROM `acme.raw.events` QUALIFY ROW_NUMBER() OVER (PARTITION BY event_date ORDER BY id) = 1",
	} {
		if got := ps.unfiltered(context.Background(), sql, []warehouse.TableRef{eventsRef}); len(got) != 1 {
			t.Errorf("%q: got %+v, want unfiltered", sql, got)
		}
	}
}

func TestSplitSQLStatementsStringsCommentsAndBlocks(t *testing.T) {
	stmts := splitSQLStatements("-- a comment; with semicolon\nDECLARE x STRING DEFAULT 'abc;def';\nIF x = 'a' THEN\n  INSERT INTO t1 VALUES (1);\nELSE\n  INSERT INTO t2 VALUES (2);\nEND IF;\nSELECT * FROM t3;")
	if len(stmts) != 3 {
		t.Fatalf("got %d stmts: %#v", len(stmts), stmts)
	}
	if !strings.Contains(stmts[0], "DEFAULT 'abc;def'") {
		t.Errorf("stmt[0] = %q", stmts[0])
	}
	if !strings.HasPrefix(stmts[1], "IF x = 'a'") || !strings.HasSuffix(stmts[1], "END IF") {
		t.Errorf("stmt[1] should keep IF...END IF intact: %q", stmts[1])
	}
	if stmts[2] != "SELECT * FROM t3" {
		t.Errorf("stmt[2] = %q", stmts[2])
	}
	// IF(...) / REPEAT(...) functions, END CASE, BEGIN TRANSACTION, and FOR...END FOR
	// must not leave blocks open.
	edge := splitSQLStatements("BEGIN TRANSACTION; SELECT CASE WHEN a THEN IF(b, 1, 2) END FROM t1; COMMIT TRANSACTION; SELECT REPEAT('x', 3) FROM t2;")
	if len(edge) != 4 {
		t.Fatalf("edge stmts = %d: %#v", len(edge), edge)
	}
}

func TestScriptSubqueryRecovery(t *testing.T) {
	// 1. Table with pre_operations whose combined script returns StatementType=SCRIPT
	// recovers a lower-bound estimate from its main query.
	tbl := &project.Action{
		ID:     "acme.marts.orders",
		Target: project.Target{Database: "acme", Schema: "marts", Name: "orders"},
		Kind:   project.KindTable,
		PreOps: []string{"DECLARE cutoff DATE DEFAULT CURRENT_DATE()"},
		Query:  "SELECT * FROM `acme.raw.events` WHERE event_date >= CURRENT_DATE()",
	}
	// 2. Multi-statement operation script recovers sum of estimable statements as LowerBound.
	op := &project.Action{
		ID:      "acme.marts.backfill",
		Target:  project.Target{Database: "acme", Schema: "marts", Name: "backfill"},
		Kind:    project.KindOperation,
		Queries: []string{"DECLARE x INT64 DEFAULT 1", "INSERT INTO `acme.marts.t1` SELECT * FROM `acme.raw.a`", "UPDATE `acme.marts.t2` SET v = 1 WHERE TRUE"},
	}
	g := project.NewGraph([]*project.Action{tbl, op})
	res := &changes.Result{
		HeadRef: "feat", BaseRef: "main", MergeBase: "abc1234",
		Diff: changes.Diff{
			Changed: []changes.Changed{
				{ID: tbl.ID, Type: "table", FileName: "orders.sqlx"},
				{ID: op.ID, Type: "operations", FileName: "backfill.sqlx"},
			},
		},
	}
	deps := Deps{
		DryRun: func(_ context.Context, _, _, sql string) (*warehouse.Check, error) {
			switch {
			case strings.Contains(sql, "DECLARE"):
				return &warehouse.Check{StatementType: "SCRIPT", Accuracy: warehouse.AccuracyUnknown}, nil
			case strings.Contains(sql, "acme.raw.events"):
				return &warehouse.Check{Bytes: 2 * gib, Accuracy: warehouse.AccuracyPrecise}, nil
			case strings.Contains(sql, "INSERT INTO"):
				return &warehouse.Check{Bytes: 3 * gib, Accuracy: warehouse.AccuracyPrecise}, nil
			case strings.Contains(sql, "UPDATE"):
				return &warehouse.Check{Bytes: 1 * gib, Accuracy: warehouse.AccuracyPrecise}, nil
			}
			return &warehouse.Check{StatementType: "SCRIPT"}, nil
		},
	}
	rep, err := Run(context.Background(), res, Head{Graph: g}, deps, nil)
	if err != nil {
		t.Fatal(err)
	}
	gotTbl := find(t, rep, "orders")
	if gotTbl.Estimate == nil || gotTbl.Estimate.Bytes != 2*gib || gotTbl.Estimate.Accuracy != warehouse.AccuracyLowerBound {
		t.Fatalf("table script recovery = %+v", gotTbl.Estimate)
	}
	gotOp := find(t, rep, "backfill")
	if gotOp.Estimate == nil || gotOp.Estimate.Bytes != 4*gib || gotOp.Estimate.Accuracy != warehouse.AccuracyLowerBound {
		t.Fatalf("op script recovery = %+v", gotOp.Estimate)
	}
}
