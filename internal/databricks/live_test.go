package databricks

import (
	"context"
	"os"
	"testing"
	"time"

	"github.com/ashish10alex/xf/internal/warehouse"
)

// The live tests run against a real workspace, such as a Free Edition one,
// and its samples catalog, only when $DATABRICKS_HOST names it: CI has no
// workspace token (ADR 0042). The token is $DATABRICKS_TOKEN or the
// Databricks CLI's. The check runs only when $DATABRICKS_WAREHOUSE names a
// SQL warehouse, which it starts, and which bills while it runs.

func liveClient(t *testing.T) *Client {
	t.Helper()
	if os.Getenv("DATABRICKS_HOST") == "" {
		t.Skip("$DATABRICKS_HOST names no workspace")
	}
	return New(Resolve(Setup{Warehouse: os.Getenv("DATABRICKS_WAREHOUSE")}).Config)
}

func TestLiveUnityCatalog(t *testing.T) {
	c := liveClient(t)
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()
	tbl, err := c.Table(ctx, "samples", "nyctaxi", "trips")
	if err != nil {
		t.Fatal(err)
	}
	if len(tbl.Schema) == 0 {
		t.Errorf("samples.nyctaxi.trips has no columns: %+v", tbl)
	}
	if _, err := c.Table(ctx, "samples", "nyctaxi", "no_such_table"); err == nil {
		t.Error("found a table that isn't there")
	}
}

func TestLiveCheck(t *testing.T) {
	c := liveClient(t)
	if c.Warehouse() == "" {
		t.Skip("$DATABRICKS_WAREHOUSE names no SQL warehouse to check on")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Minute)
	defer cancel()
	res, err := c.Check(ctx, "", "", "select tpep_pickup_datetime, fare_amount from samples.nyctaxi.trips")
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Schema) != 2 || res.Schema[0].Name != "tpep_pickup_datetime" {
		t.Errorf("schema = %+v", res.Schema)
	}
	if _, err := c.Check(ctx, "", "", "select nope from samples.nyctaxi.trips"); err == nil {
		t.Error("a query naming no column passed")
	} else {
		t.Logf("error, as expected: %v", err)
	}
}

func TestLiveQuery(t *testing.T) {
	c := liveClient(t)
	if c.Warehouse() == "" {
		t.Skip("$DATABRICKS_WAREHOUSE names no SQL warehouse to query on")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Minute)
	defer cancel()
	ref, err := c.StartQuery(ctx, warehouse.QueryOptions{Project: "samples",
		SQL:    "select trip_distance, fare_amount from nyctaxi.trips where pickup_zip = :zip limit 1200",
		Params: map[string]string{"zip": "10001"}})
	if err != nil {
		t.Fatal(err)
	}
	for {
		st, err := c.QueryJob(ctx, ref)
		if err != nil {
			t.Fatal(err)
		}
		if st.State == "DONE" {
			if st.Error != "" {
				t.Fatal(st.Error)
			}
			break
		}
		time.Sleep(time.Second)
	}
	var rows int
	for token := ""; ; {
		page, err := c.Rows(ctx, ref, token, 500)
		if err != nil {
			t.Fatal(err)
		}
		rows += len(page.Rows)
		if token = page.NextToken; token == "" {
			if int64(rows) != page.Total {
				t.Errorf("read %d rows of %d", rows, page.Total)
			}
			break
		}
	}
	t.Logf("%d rows", rows)
}
