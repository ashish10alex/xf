package web

import (
	"cmp"
	"context"
	"errors"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/ashish10alex/xf/internal/project"
	"github.com/ashish10alex/xf/internal/warehouse"
)

// A dbt project on Databricks (ADR 0042). Its SQL warehouse bills while it
// runs, so xf web checks a model only when asked (K, or the palette's
// Databricks: check): DESCRIBE QUERY gives the columns its SQL returns, and
// EXPLAIN its error. A stopped warehouse is started by the check, and the
// check's line says so while it waits. A built table's columns come from
// Unity Catalog, which wakes no warehouse, as the table lookups do.

// Snowflake is the configuration of a Snowflake workspace, sharing Databricks'
// on-demand warehouse interface (ADR 0044).
type Snowflake = Databricks

// Databricks is how xf web reaches an on-demand warehouse project (Databricks
// or Snowflake): its client, links, warehouses and standalone profiles.
type Databricks struct {
	// TargetType is the type of the dbt target the project compiles with,
	// which says the warehouse until a compile does.
	TargetType string
	// Client is the workspace's client; it checks on the SQL/virtual
	// warehouse, and is a warehouse.Waker.
	Client func(ctx context.Context) (warehouse.Warehouse, error)
	// Host is the workspace's URL, for links; "" when not known.
	Host func() string
	// TableURL, when set, overrides the table link (Snowsight for Snowflake).
	TableURL func(project.Target) string
	// Warehouses lists the workspace's SQL or virtual warehouses for the form
	// a check shows when none is chosen; Choose saves the pick to the
	// project's config file, used from the next check, and answers with the
	// file.
	Warehouses func(ctx context.Context) ([]SQLWarehouse, error)
	Choose     func(id string) (file string, err error)
	// Chosen says a SQL/virtual warehouse is chosen; nil says one is.
	Chosen func() bool
	// WarehouseID returns the currently chosen SQL/virtual warehouse ID or name, "" for none.
	WarehouseID func() string
	// Profiles lists available CLI profiles/connections and returns the active
	// name; nil when profile switching isn't offered (dbt projects follow
	// profiles.yml).
	Profiles func() (profiles []DatabricksProfile, current string)
	// ChooseProfile switches the active profile/connection for standalone
	// scratch.
	ChooseProfile func(name string) (message string, err error)
	// SaveNote explains where a chosen warehouse is saved, shown in the
	// warehouse picker; "" uses the default project-config note.
	SaveNote string
	// ConfigKey is the project config key Choose writes (default databricksWarehouse).
	ConfigKey string
	// CatalogName names where built table columns come from (default Unity Catalog).
	CatalogName string
	// WarehouseLabel names the compute resource (default "SQL warehouse").
	WarehouseLabel string
	// PaletteCommand names the command that picks a warehouse.
	PaletteCommand string
	// AuthHint says how to sign in, for a check without credentials.
	AuthHint string
}

func (d *Databricks) configKey() string { return cmp.Or(d.ConfigKey, "databricksWarehouse") }
func (d *Databricks) catalogName() string {
	return cmp.Or(d.CatalogName, "Unity Catalog")
}
func (d *Databricks) warehouseLabel() string {
	return cmp.Or(d.WarehouseLabel, "SQL warehouse")
}
func (d *Databricks) paletteCommand() string {
	return cmp.Or(d.PaletteCommand, "Databricks: choose the SQL warehouse")
}

// DatabricksProfile is a ~/.databrickscfg profile listed by the profile picker.
type DatabricksProfile struct {
	Name string `json:"name"`
	Host string `json:"host,omitempty"`
}

// SQLWarehouse is a SQL warehouse the form lists.
type SQLWarehouse struct {
	ID    string `json:"id"`
	Name  string `json:"name"`
	State string `json:"state"`
	Size  string `json:"size,omitempty"`
	// Serverless starts in seconds rather than minutes.
	Serverless bool `json:"serverless,omitempty"`
}

// checkTimeout bounds a check, which may wait for a warehouse to start.
const checkTimeout = 10 * time.Minute

// targetTypeLocked is the dbt target type known before a compile finishes.
func (p *projectState) targetTypeLocked() string {
	if p.sf != nil && p.sf.TargetType != "" {
		return p.sf.TargetType
	}
	if p.dbx != nil && p.dbx.TargetType != "" {
		return p.dbx.TargetType
	}
	return ""
}

// warehouseKindLocked says which warehouse the project builds in now.
func (p *projectState) warehouseKindLocked() string {
	k := p.st.Graph.WarehouseOr(p.targetTypeLocked())
	switch k {
	case project.Databricks:
		if p.dbx != nil {
			return project.Databricks
		}
	case project.Snowflake:
		if p.sf != nil {
			return project.Snowflake
		}
	}
	return project.BigQuery
}

func (p *projectState) warehouseKind() string {
	switch {
	case p.onDbx.Load():
		return project.Databricks
	case p.onSf.Load():
		return project.Snowflake
	default:
		return project.BigQuery
	}
}

// onDemandLocked returns the active on-demand warehouse config (Databricks or
// Snowflake), or nil for BigQuery.
func (p *projectState) onDemandLocked() *Databricks {
	switch p.warehouseKindLocked() {
	case project.Databricks:
		return p.dbx
	case project.Snowflake:
		return p.sf
	}
	return nil
}

func (p *projectState) onDemand() *Databricks {
	switch {
	case p.onDbx.Load():
		return p.dbx
	case p.onSf.Load():
		return p.sf
	}
	return nil
}

func (p *projectState) isOnDemandLocked() bool { return p.onDemandLocked() != nil }
func (p *projectState) isOnDemand() bool       { return p.onDbx.Load() || p.onSf.Load() }

// databricksLocked says the project builds in Databricks: its graph says
// so, or before a compile its dbt target.
func (p *projectState) databricksLocked() bool {
	return p.warehouseKindLocked() == project.Databricks
}

func (p *projectState) databricks() bool {
	return p.onDbx.Load()
}

// checkAction runs the on-demand check of an action's SQL, as compiled:
// each of its queries in turn. It answers at once; the results come as
// detail and state events.
func (p *projectState) checkAction(w http.ResponseWriter, r *http.Request) {
	var req struct {
		ID string `json:"id"`
	}
	if !readJSON(w, r, &req) {
		return
	}
	if od := p.onDemand(); od != nil && od.Chosen != nil && !od.Chosen() {
		writeJSON(w, okView{Kind: "warehouse", Message: "choose the " + od.warehouseLabel() + " checks run on"})
		return
	}
	p.mu.Lock()
	g := p.st.Graph
	var a *project.Action
	if g != nil {
		a = g.Actions[project.ActionID(req.ID)]
	}
	od := p.onDemandLocked()
	why := ""
	switch {
	case od == nil:
		why = "checks are on Databricks and Snowflake; this project dry runs on its own"
	case g == nil || a == nil:
		why = "no such action"
	case g.Notice != "":
		why = "not compiled: " + g.Notice
	case p.st.Outdated != "" || p.sqlNoteLocked(a) != "":
		why = "waiting for the compile…"
	case len(a.DryRunQueries()) == 0:
		why = a.Kind.String() + ": nothing to check"
	}
	if why != "" {
		p.mu.Unlock()
		writeJSON(w, okView{Message: why})
		return
	}
	proj, location := g.DryRunTarget(a)
	var entries []*dryEntry
	var sqls []string
	for _, q := range a.DryRunQueries() {
		key := dryRunKey(proj, location, q.SQL)
		e := p.dry[key]
		if e != nil && e.loading {
			continue
		}
		e = &dryEntry{loading: true, asked: true}
		p.dry[key] = e
		// The model's query alone: single-statement check, not hooks.
		sql := a.Query
		if q.Label == "incremental" {
			sql = a.IncrementalQuery
		}
		entries, sqls = append(entries, e), append(sqls, cmp.Or(sql, q.SQL))
	}
	v := p.stateViewLocked()
	p.mu.Unlock()
	if len(entries) == 0 {
		writeJSON(w, okView{Message: "already checking"})
		return
	}
	p.hub.send("detail", nil)
	p.hub.send("state", v)
	go p.runChecks(od, entries, proj, location, sqls)
	writeJSON(w, okView{OK: true})
}

// runChecks checks sqls on the SQL/virtual warehouse, telling the page each
// answer, and first whether the warehouse is starting.
func (p *projectState) runChecks(od *Databricks, entries []*dryEntry, proj, location string, sqls []string) {
	ctx, cancel := context.WithTimeout(context.Background(), checkTimeout)
	defer cancel()
	finish := func(e *dryEntry, res *warehouse.Check, err error) {
		p.mu.Lock()
		e.loading, e.done, e.at, e.res, e.err = false, true, p.deps.Now(), res, err
		v := p.stateViewLocked()
		p.mu.Unlock()
		p.hub.send("detail", nil)
		p.hub.send("state", v)
	}
	c, err := od.Client(ctx)
	if err == nil {
		if wk, ok := c.(warehouse.Waker); ok {
			up, werr := wk.Running(ctx)
			switch {
			case werr != nil:
				err = werr
			case !up:
				p.mu.Lock()
				for _, e := range entries {
					e.woke = true
				}
				p.mu.Unlock()
				p.hub.send("detail", nil)
			}
		}
	}
	for i, e := range entries {
		if err != nil {
			finish(e, nil, err)
			continue
		}
		res, cerr := c.Check(ctx, proj, location, sqls[i])
		finish(e, res, cerr)
	}
}

// checkDetailLocked is the line under an on-demand model's row for one of
// its queries: not checked yet, checking (and the warehouse starting), or
// what the check said.
func (p *projectState) checkDetailLocked(e *dryEntry, label string) detail {
	od := p.onDemandLocked()
	whLabel, authHint := "SQL warehouse", ""
	if od != nil {
		whLabel, authHint = od.warehouseLabel(), od.AuthHint
	}
	d := detail{Label: cmp.Or(label, "check"), Dry: true, Tone: "dim"}
	starting := startingNote(e)
	switch {
	case e == nil || !e.asked:
		d.Value = "not checked · K checks on the " + whLabel
	case e.loading:
		d.Value = "checking…" + starting
		if starting != "" {
			d.Tone = "warn"
		}
	case errors.Is(e.err, warehouse.ErrNoWarehouse):
		d.Value, d.Tone = "no "+whLabel+" chosen: K picks one", "warn"
	case errors.Is(e.err, warehouse.ErrAuth):
		d.Value, d.Tone = "no credentials: "+authHint, "warn"
	case e.err != nil:
		msg := e.err.Error()
		if apiErr, ok := errors.AsType[*warehouse.APIError](e.err); ok {
			msg = apiErr.Message
		}
		d.Value, d.Tone = "✗ "+firstLine(msg), "error"
	default:
		d.Value, d.Tone = "✓ "+plural(len(e.res.Schema), "column")+" · "+e.at.Local().Format("15:04")+strings.Replace(starting, "starting", "started", 1), "ok"
	}
	return d
}

// startingNote is what the check's line adds while e's check waits for the
// warehouse to start: it bills from then (ADR 0042).
func startingNote(e *dryEntry) string {
	if e != nil && e.woke {
		return " · warehouse starting"
	}
	return ""
}

// checkColumnsLocked is an on-demand model's columns: its check's, when
// one has answered for the SQL as it is, else its built table's from its
// catalog, else why there are none.
func (p *projectState) checkColumnsLocked(a *project.Action, e *dryEntry, built *tableEntry) columnsView {
	od := p.onDemandLocked()
	whLabel, catName, authHint := "SQL warehouse", "Unity Catalog", ""
	if od != nil {
		whLabel, catName, authHint = od.warehouseLabel(), od.catalogName(), od.AuthHint
	}
	why := func(reason string, failed bool) columnsView {
		return columnsView{Fields: []warehouse.Column{}, Reason: reason, Failed: failed}
	}
	switch {
	case e != nil && e.asked && e.loading:
		return why("checking…"+startingNote(e), false)
	case e != nil && e.asked && e.done && e.err == nil:
		v := p.describedLocked(a, e.res.Schema)
		v.Note = "from the check of the SQL as compiled at " + e.at.Local().Format("15:04")
		return v
	case e != nil && e.asked && e.done && errors.Is(e.err, warehouse.ErrNoWarehouse):
		return why("no "+whLabel+" chosen: K picks one", true)
	case e != nil && e.asked && e.done:
		return why("✗ "+firstLine(checkMessage(e.err))+" — press e for details", true)
	case built != nil && built.done && built.err == nil && built.tbl != nil && len(built.tbl.Schema) > 0:
		v := p.describedLocked(a, built.tbl.Schema)
		v.Note = "the built table's, from " + catName + " · K checks the edited SQL's"
		return v
	case built == nil || !built.done:
		return why("looking up the built table…", false)
	case warehouse.IsNotFound(built.err):
		return why("not built yet · K checks its SQL on the "+whLabel+" for its columns", false)
	case errors.Is(built.err, warehouse.ErrAuth):
		return why("no credentials: "+authHint, true)
	case built.err != nil:
		return why("✗ "+firstLine(checkMessage(built.err)), true)
	}
	return why("the built table has no columns · K checks its SQL", false)
}

func checkMessage(err error) string {
	if apiErr, ok := errors.AsType[*warehouse.APIError](err); ok {
		return apiErr.Message
	}
	return err.Error()
}

// describedLocked is cols with the descriptions a's config gives where the
// warehouse gives none.
func (p *projectState) describedLocked(a *project.Action, cols []warehouse.Column) columnsView {
	desc := map[string]string{}
	for _, c := range a.Columns {
		desc[strings.Join(c.Path, ".")] = c.Description
	}
	fields := append([]warehouse.Column{}, cols...)
	for i := range fields {
		if fields[i].Description == "" {
			fields[i].Description = desc[fields[i].Name]
		}
	}
	return columnsView{Fields: fields}
}

// catalogURL is the table's page in the workspace's Catalog Explorer, ""
// when the workspace isn't known.
func (p *projectState) catalogURL(t project.Target) string {
	if p.dbx == nil || p.dbx.Host == nil || t.Name == "" {
		return ""
	}
	host := p.dbx.Host()
	if host == "" {
		return ""
	}
	return host + "/explore/data/" + url.PathEscape(t.Database) + "/" + url.PathEscape(t.Schema) + "/" + url.PathEscape(t.Name)
}

// tableURL is where u opens a table: Catalog Explorer for Databricks,
// Snowsight for Snowflake, else the BigQuery console.
func (p *projectState) tableURL(t project.Target) string {
	if od := p.onDemand(); od != nil {
		if od.TableURL != nil {
			return od.TableURL(t)
		}
		return p.catalogURL(t)
	}
	return t.ConsoleURL()
}

// databricksWarehouses lists the SQL or virtual warehouses for the form.
func (p *projectState) databricksWarehouses(w http.ResponseWriter, r *http.Request) {
	od := p.onDemand()
	if od == nil || od.Warehouses == nil {
		http.Error(w, "not an on-demand warehouse project", http.StatusNotFound)
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), time.Minute)
	defer cancel()
	list, err := od.Warehouses(ctx)
	cur := ""
	if od.WarehouseID != nil {
		cur = od.WarehouseID()
	}
	v := struct {
		Warehouses []SQLWarehouse `json:"warehouses"`
		Current    string         `json:"current,omitempty"`
		SaveNote   string         `json:"saveNote,omitempty"`
		Error      string         `json:"error,omitempty"`
	}{Warehouses: nonNil(list), Current: cur, SaveNote: od.SaveNote}
	if err != nil {
		v.Error = err.Error()
		if errors.Is(err, warehouse.ErrAuth) {
			v.Error = "no credentials: " + od.AuthHint
		}
	}
	writeJSON(w, v)
}

// chooseDatabricksWarehouse saves the form's pick, and has the checks that
// failed for want of one asked again.
func (p *projectState) chooseDatabricksWarehouse(w http.ResponseWriter, r *http.Request) {
	var req struct {
		ID string `json:"id"`
	}
	if !readJSON(w, r, &req) {
		return
	}
	od := p.onDemand()
	if od == nil || od.Choose == nil || req.ID == "" {
		http.Error(w, "bad request", http.StatusBadRequest)
		return
	}
	file, err := od.Choose(req.ID)
	if err != nil {
		writeJSON(w, okView{Message: err.Error()})
		return
	}
	p.mu.Lock()
	for k, e := range p.dry {
		if e.done && errors.Is(e.err, warehouse.ErrNoWarehouse) {
			delete(p.dry, k)
		}
	}
	p.mu.Unlock()
	p.hub.send("detail", nil)
	if p.standalone {
		p.hub.send("databricks", map[string]any{"warehouse": req.ID})
	}
	msg := od.configKey() + " saved to " + file
	if p.standalone {
		msg = od.warehouseLabel() + " saved to " + file
	}
	writeJSON(w, okView{OK: true, Message: msg})
}

// databricksProfiles lists the CLI profiles/connections available to
// standalone Databricks or Snowflake scratch and reports the current state.
func (p *projectState) databricksProfiles(w http.ResponseWriter, r *http.Request) {
	od := p.onDemand()
	if od == nil || od.Profiles == nil {
		http.Error(w, "profile switching is only available for standalone scratch", http.StatusNotFound)
		return
	}
	profiles, current := od.Profiles()
	host, wh := "", ""
	if od.Host != nil {
		host = od.Host()
	}
	if od.WarehouseID != nil {
		wh = od.WarehouseID()
	}
	writeJSON(w, struct {
		Profiles     []DatabricksProfile `json:"profiles"`
		Current      string              `json:"current,omitempty"`
		Host         string              `json:"host,omitempty"`
		Warehouse    string              `json:"warehouse,omitempty"`
		NeedsProfile bool                `json:"needsProfile,omitempty"`
		AuthHint     string              `json:"authHint,omitempty"`
	}{
		Profiles:     nonNil(profiles),
		Current:      current,
		Host:         host,
		Warehouse:    wh,
		NeedsProfile: host == "",
		AuthHint:     od.AuthHint,
	})
}

// chooseDatabricksProfile switches the active CLI profile/connection for
// standalone scratch, clearing cached metadata from the previous workspace.
func (p *projectState) chooseDatabricksProfile(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Name string `json:"name"`
	}
	if !readJSON(w, r, &req) {
		return
	}
	od := p.onDemand()
	if od == nil || od.ChooseProfile == nil || req.Name == "" {
		http.Error(w, "bad request", http.StatusBadRequest)
		return
	}
	msg, err := od.ChooseProfile(req.Name)
	if err != nil {
		writeJSON(w, okView{Message: err.Error()})
		return
	}
	p.meta.clear()
	p.mu.Lock()
	clear(p.dry)
	p.mu.Unlock()
	host, wh := "", ""
	if od.Host != nil {
		host = od.Host()
	}
	if od.WarehouseID != nil {
		wh = od.WarehouseID()
	}
	chosen := wh != ""
	if od.Chosen != nil {
		chosen = od.Chosen()
	}
	p.hub.send("databricks", map[string]any{
		"profile":      req.Name,
		"host":         host,
		"warehouse":    wh,
		"needsProfile": host == "",
	})
	writeJSON(w, struct {
		OK        bool   `json:"ok"`
		Message   string `json:"message,omitempty"`
		Current   string `json:"current,omitempty"`
		Host      string `json:"host,omitempty"`
		Warehouse string `json:"warehouse,omitempty"`
		Chosen    bool   `json:"chosen"`
	}{OK: true, Message: msg, Current: req.Name, Host: host, Warehouse: wh, Chosen: chosen})
}
