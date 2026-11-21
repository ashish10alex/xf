package main

import (
	"cmp"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/atotto/clipboard"

	"github.com/ashish10alex/xf/internal/config"
	"github.com/ashish10alex/xf/internal/databricks"
	"github.com/ashish10alex/xf/internal/engine"
	"github.com/ashish10alex/xf/internal/frontend"
	"github.com/ashish10alex/xf/internal/project"
	"github.com/ashish10alex/xf/internal/snowflake"
	"github.com/ashish10alex/xf/internal/warehouse"
	"github.com/ashish10alex/xf/internal/web"
)

// noEngine is the engine of xf scratch outside a project: it compiles
// nothing, and reports a project with no actions.
type noEngine struct{}

func (noEngine) Subscribe() (<-chan engine.State, func()) {
	states := make(chan engine.State, 1)
	states <- engine.State{Graph: project.NewGraph(nil), Compiles: 1, CompiledAt: time.Now()}
	return states, func() {}
}
func (noEngine) Recompile(string)       {}
func (noEngine) Focus(project.ActionID) {}
func (noEngine) SetTheme(string)        {}

// scratchHome is where scratches live outside a project: tmp in xf's user
// config folder, where the user's config.json is (~/.config/xf, or
// %AppData%\xf on Windows).
func scratchHome() (string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(config.UserDir(os.Getenv, home), "tmp"), nil
}

// flagValue returns the CLI flag value for key when sf registered it.
func (sf *settingFlags) flagValue(key string) string {
	if sf == nil || sf.values == nil {
		return ""
	}
	if p := sf.values[key]; p != nil {
		return strings.TrimSpace(*p)
	}
	return ""
}

// normalizeScratchWarehouse parses --warehouse values.
func normalizeScratchWarehouse(raw string) (string, error) {
	switch strings.ToLower(strings.TrimSpace(raw)) {
	case "":
		return "", nil
	case project.BigQuery, "bq":
		return project.BigQuery, nil
	case project.Databricks, "dbx":
		return project.Databricks, nil
	case project.Snowflake, "sf":
		return project.Snowflake, nil
	default:
		return "", fmt.Errorf("--warehouse must be bigquery, databricks or snowflake, not %q", raw)
	}
}

// inferScratchWarehouse picks a standalone scratch warehouse from
// warehouse-specific CLI flags when --warehouse was omitted outside a project.
func inferScratchWarehouse(sf *settingFlags, projectID, location string) string {
	if projectID != "" || location != "" {
		return project.BigQuery
	}
	if sf.flagValue(config.KeyDatabricksProfile) != "" || sf.flagValue(config.KeyDatabricksWarehouse) != "" {
		return project.Databricks
	}
	if sf.flagValue(config.KeySnowflakeConnection) != "" || sf.flagValue(config.KeySnowflakeWarehouse) != "" {
		return project.Snowflake
	}
	return ""
}

// standaloneScratch opens a standalone scratch workspace outside any project
// for wh ("bigquery" or "databricks"): no models and no engine, only the
// warehouse client and a folder for scratches. applyFlags is true for the
// initial CLI launch and false when reopening from the picker, so a profile or
// warehouse chosen in the UI isn't overridden on reopen.
func standaloneScratch(sf *settingFlags, wh, projectID, location string, applyFlags bool) (*web.Project, error) {
	wh, err := normalizeScratchWarehouse(wh)
	if err != nil {
		return nil, err
	}
	if wh == "" {
		wh = project.BigQuery
	}
	settings, err := sf.load("")
	if err != nil {
		return nil, err
	}
	home, err := scratchHome()
	if err != nil {
		return nil, err
	}
	deps := frontend.Deps{
		Engine:      noEngine{},
		Copy:        clipboard.WriteAll,
		OpenURL:     browserOpener(),
		Theme:       settings.Theme,
		SaveTheme:   saveTheme,
		CommandUses: commandUses(),
	}
	p := &web.Project{
		Standalone:     true,
		StartScratch:   true,
		SqlfluffBin:    settings.SqlfluffBin,
		SqlfluffConfig: settings.SqlfluffConfig,
		WrapLines:      settings.WrapLines == "true",
		VimMode:        settings.VimMode == "true",
		VimKeys:        vimKeys(settings.Settings),
	}
	p.ScratchWarnGB, _ = strconv.ParseFloat(settings.ScratchWarnGB, 64)
	p.ScratchConfirmGB, _ = strconv.ParseFloat(settings.ScratchConfirmGB, 64)

	if wh == project.Databricks {
		dir := filepath.Join(home, "databricks")
		var dbxFlags *settingFlags
		if applyFlags {
			dbxFlags = sf
		}
		sd := newStandaloneDatabricks(dir, dbxFlags, settings.Settings)
		dbx := sd.webDatabricks()
		deps.Root = dir
		deps.Table = lazyTables(dbx.Client)
		p.Deps = deps
		p.Databricks = dbx
		p.ScratchDir = dir
		return p, nil
	}

	if wh == project.Snowflake {
		dir := filepath.Join(home, "snowflake")
		var sfFlags *settingFlags
		if applyFlags {
			sfFlags = sf
		}
		ss := newStandaloneSnowflake(dir, sfFlags, settings.Settings)
		wsf := ss.webSnowflake()
		deps.Root = dir
		deps.Table = lazyTables(wsf.Client)
		p.Deps = deps
		p.Snowflake = wsf
		p.ScratchDir = dir
		return p, nil
	}

	bq := lazyClient()
	deps.Root = home
	deps.DryRun = dryRunner(bq)
	deps.Table = lazyTables(bq)
	p.Deps = deps
	p.Warehouse = bq
	p.ScratchDir = home
	p.ScratchProject = projectID
	p.ScratchLocation = location
	return p, nil
}

// standaloneDbxState is what standalone Databricks scratch keeps in
// ~/.config/xf/tmp/databricks/connection.json: the active ~/.databrickscfg
// profile and the SQL warehouse chosen per profile (so standalone picks never
// overwrite ~/.config/xf/config.json or override dbt projects' profiles.yml).
type standaloneDbxState struct {
	Profile    string            `json:"profile,omitempty"`
	Warehouses map[string]string `json:"warehouses,omitempty"`
}

const standaloneDbxFile = "connection.json"

type standaloneDatabricks struct {
	dir           string
	flagProfile   string
	flagWarehouse string
	cfgProfile    string
	cfgWarehouse  string
	configFile    string
	getenv        func(string) string

	mu      sync.Mutex
	state   standaloneDbxState
	profile string
	client  *databricks.Client
}

func newStandaloneDatabricks(dir string, sf *settingFlags, s config.Settings) *standaloneDatabricks {
	sd := &standaloneDatabricks{
		dir:           dir,
		flagProfile:   sf.flagValue(config.KeyDatabricksProfile),
		flagWarehouse: sf.flagValue(config.KeyDatabricksWarehouse),
		cfgProfile:    s.DatabricksProfile,
		cfgWarehouse:  s.DatabricksWarehouse,
		getenv:        os.Getenv,
	}
	sd.state = sd.loadState()
	sd.rebuildLocked()
	return sd
}

func (sd *standaloneDatabricks) statePath() string {
	return filepath.Join(sd.dir, standaloneDbxFile)
}

func (sd *standaloneDatabricks) loadState() standaloneDbxState {
	var st standaloneDbxState
	data, err := os.ReadFile(sd.statePath())
	if err == nil {
		_ = json.Unmarshal(data, &st)
	}
	if st.Warehouses == nil {
		st.Warehouses = map[string]string{}
	}
	return st
}

func (sd *standaloneDatabricks) saveStateLocked() (string, error) {
	if err := os.MkdirAll(sd.dir, 0o755); err != nil {
		return "", err
	}
	data, err := json.MarshalIndent(sd.state, "", "  ")
	if err != nil {
		return "", err
	}
	p := sd.statePath()
	tmp := p + ".tmp"
	if err := os.WriteFile(tmp, append(data, '\n'), 0o644); err != nil {
		return "", err
	}
	if err := os.Rename(tmp, p); err != nil {
		return "", err
	}
	return p, nil
}

func (sd *standaloneDatabricks) warehouseKeyLocked(profile, host string) string {
	return cmp.Or(profile, host, "default")
}

// rebuildLocked resolves the Databricks connection for standalone scratch and
// builds a fresh client. Precedence: CLI flag -> saved standalone state ->
// user config -> DEFAULT / $DATABRICKS_HOST -> unambiguous single profile in
// ~/.databrickscfg.
func (sd *standaloneDatabricks) rebuildLocked() {
	profile := cmp.Or(sd.flagProfile, sd.state.Profile, sd.cfgProfile)
	conn := databricks.Resolve(databricks.Setup{
		Profile:    profile,
		ConfigFile: sd.configFile,
		Getenv:     sd.getenv,
	})
	// If no host resolved (no DEFAULT with host, no $DATABRICKS_HOST) and
	// ~/.databrickscfg has exactly one workspace profile, use it.
	if conn.Host == "" && profile == "" {
		if list := databricks.ListProfiles(sd.configFile, sd.getenv); len(list) == 1 {
			profile = list[0].Name
			conn = databricks.Resolve(databricks.Setup{
				Profile:    profile,
				ConfigFile: sd.configFile,
				Getenv:     sd.getenv,
			})
		}
	}
	key := sd.warehouseKeyLocked(conn.Profile, conn.Host)
	wh := sd.flagWarehouse
	if wh == "" {
		wh = sd.state.Warehouses[key]
	}
	// Only inherit user-level databricksWarehouse when neither a CLI profile
	// flag nor a saved standalone profile selected a different workspace.
	if wh == "" && sd.flagProfile == "" && sd.state.Profile == "" {
		wh = sd.cfgWarehouse
	}
	conn.Warehouse = databricks.WarehouseID(wh)
	sd.profile = conn.Profile
	sd.client = databricks.New(conn.Config)
}

func (sd *standaloneDatabricks) currentClient() *databricks.Client {
	sd.mu.Lock()
	defer sd.mu.Unlock()
	return sd.client
}

func (sd *standaloneDatabricks) webDatabricks() *web.Databricks {
	return &web.Databricks{
		TargetType: project.Databricks,
		Client: func(ctx context.Context) (warehouse.Warehouse, error) {
			return sd.currentClient(), nil
		},
		Host: func() string { return sd.currentClient().Host() },
		Warehouses: func(ctx context.Context) ([]web.SQLWarehouse, error) {
			list, err := sd.currentClient().Warehouses(ctx)
			out := make([]web.SQLWarehouse, 0, len(list))
			for _, s := range list {
				out = append(out, web.SQLWarehouse{ID: s.ID, Name: s.Name, State: s.State, Size: s.Size, Serverless: s.Serverless})
			}
			return out, err
		},
		Choose: func(id string) (string, error) {
			sd.mu.Lock()
			defer sd.mu.Unlock()
			if sd.state.Warehouses == nil {
				sd.state.Warehouses = map[string]string{}
			}
			key := sd.warehouseKeyLocked(sd.profile, sd.client.Host())
			sd.state.Warehouses[key] = id
			sd.flagWarehouse = ""
			file, err := sd.saveStateLocked()
			if err != nil {
				return "", err
			}
			sd.client.SetWarehouse(id)
			return file, nil
		},
		Chosen:      func() bool { return sd.currentClient().Warehouse() != "" },
		WarehouseID: func() string { return sd.currentClient().Warehouse() },
		Profiles: func() ([]web.DatabricksProfile, string) {
			raw := databricks.ListProfiles(sd.configFile, sd.getenv)
			out := make([]web.DatabricksProfile, 0, len(raw))
			for _, p := range raw {
				out = append(out, web.DatabricksProfile{Name: p.Name, Host: p.Host})
			}
			sd.mu.Lock()
			cur := sd.profile
			sd.mu.Unlock()
			return out, cur
		},
		ChooseProfile: func(name string) (string, error) {
			raw := databricks.ListProfiles(sd.configFile, sd.getenv)
			found := false
			for _, p := range raw {
				if p.Name == name {
					found = true
					break
				}
			}
			if !found {
				return "", fmt.Errorf("no Databricks profile %q in ~/.databrickscfg", name)
			}
			sd.mu.Lock()
			defer sd.mu.Unlock()
			sd.state.Profile = name
			sd.flagProfile = ""
			sd.flagWarehouse = ""
			if _, err := sd.saveStateLocked(); err != nil {
				return "", err
			}
			sd.rebuildLocked()
			return "switched to Databricks profile " + name, nil
		},
		SaveNote: "Saved for standalone Databricks scratch (~/.config/xf/tmp/databricks/connection.json).",
		AuthHint: databricks.AuthHint,
	}
}

// standaloneSnowflake manages connection and virtual warehouse state for
// standalone Snowflake scratch (~/.config/xf/tmp/snowflake/connection.json).
type standaloneSnowflake struct {
	dir             string
	flagConnection  string
	flagWarehouse   string
	cfgConnection   string
	cfgWarehouse    string
	connectionsFile string
	getenv          func(string) string

	mu         sync.Mutex
	state      standaloneDbxState
	connection string
	client     *snowflake.Client
}

func newStandaloneSnowflake(dir string, sf *settingFlags, s config.Settings) *standaloneSnowflake {
	ss := &standaloneSnowflake{
		dir:            dir,
		flagConnection: sf.flagValue(config.KeySnowflakeConnection),
		flagWarehouse:  sf.flagValue(config.KeySnowflakeWarehouse),
		cfgConnection:  s.SnowflakeConnection,
		cfgWarehouse:   s.SnowflakeWarehouse,
		getenv:         os.Getenv,
	}
	ss.state = ss.loadState()
	ss.rebuildLocked()
	return ss
}

func (ss *standaloneSnowflake) statePath() string { return filepath.Join(ss.dir, standaloneDbxFile) }

func (ss *standaloneSnowflake) loadState() standaloneDbxState {
	var st standaloneDbxState
	if data, err := os.ReadFile(ss.statePath()); err == nil {
		_ = json.Unmarshal(data, &st)
	}
	if st.Warehouses == nil {
		st.Warehouses = map[string]string{}
	}
	return st
}

func (ss *standaloneSnowflake) saveStateLocked() (string, error) {
	if err := os.MkdirAll(ss.dir, 0o755); err != nil {
		return "", err
	}
	data, err := json.MarshalIndent(ss.state, "", "  ")
	if err != nil {
		return "", err
	}
	p := ss.statePath()
	tmp := p + ".tmp"
	if err := os.WriteFile(tmp, append(data, '\n'), 0o644); err != nil {
		return "", err
	}
	if err := os.Rename(tmp, p); err != nil {
		return "", err
	}
	return p, nil
}

func (ss *standaloneSnowflake) warehouseKeyLocked(conn, host string) string {
	return cmp.Or(conn, host, "default")
}

func (ss *standaloneSnowflake) rebuildLocked() {
	connName := cmp.Or(ss.flagConnection, ss.state.Profile, ss.cfgConnection)
	conn := snowflake.Resolve(snowflake.Setup{
		Connection:      connName,
		ConnectionsFile: ss.connectionsFile,
		Getenv:          ss.getenv,
	})
	if conn.Host == "" && connName == "" {
		if list := snowflake.ListConnections(ss.connectionsFile, ss.getenv); len(list) == 1 {
			connName = list[0].Name
			conn = snowflake.Resolve(snowflake.Setup{
				Connection:      connName,
				ConnectionsFile: ss.connectionsFile,
				Getenv:          ss.getenv,
			})
		}
	}
	key := ss.warehouseKeyLocked(conn.ConnectionName, conn.Host)
	wh := ss.flagWarehouse
	if wh == "" {
		wh = ss.state.Warehouses[key]
	}
	if wh == "" && ss.flagConnection == "" && ss.state.Profile == "" {
		wh = ss.cfgWarehouse
	}
	if wh != "" {
		conn.Warehouse = wh
	}
	ss.connection = conn.ConnectionName
	ss.client = snowflake.New(conn.Config)
}

func (ss *standaloneSnowflake) currentClient() *snowflake.Client {
	ss.mu.Lock()
	defer ss.mu.Unlock()
	return ss.client
}

func (ss *standaloneSnowflake) webSnowflake() *web.Snowflake {
	return &web.Snowflake{
		TargetType: project.Snowflake,
		Client: func(ctx context.Context) (warehouse.Warehouse, error) {
			return ss.currentClient(), nil
		},
		Host: func() string { return ss.currentClient().Host() },
		TableURL: func(t project.Target) string {
			c := ss.currentClient()
			return snowflake.SnowsightURL(cmp.Or(c.Account(), c.Host()), t.Database, t.Schema, t.Name)
		},
		Warehouses: func(ctx context.Context) ([]web.SQLWarehouse, error) {
			list, err := ss.currentClient().Warehouses(ctx)
			out := make([]web.SQLWarehouse, 0, len(list))
			for _, s := range list {
				out = append(out, web.SQLWarehouse{ID: s.Name, Name: s.Name, State: s.State, Size: s.Size, Serverless: true})
			}
			return out, err
		},
		Choose: func(name string) (string, error) {
			ss.mu.Lock()
			defer ss.mu.Unlock()
			if ss.state.Warehouses == nil {
				ss.state.Warehouses = map[string]string{}
			}
			key := ss.warehouseKeyLocked(ss.connection, ss.client.Host())
			ss.state.Warehouses[key] = name
			ss.flagWarehouse = ""
			file, err := ss.saveStateLocked()
			if err != nil {
				return "", err
			}
			ss.client.SetWarehouse(name)
			return file, nil
		},
		Chosen:      func() bool { return ss.currentClient().Warehouse() != "" },
		WarehouseID: func() string { return ss.currentClient().Warehouse() },
		Profiles: func() ([]web.DatabricksProfile, string) {
			raw := snowflake.ListConnections(ss.connectionsFile, ss.getenv)
			out := make([]web.DatabricksProfile, 0, len(raw))
			for _, p := range raw {
				out = append(out, web.DatabricksProfile{Name: p.Name, Host: cmp.Or(p.Account, p.Host)})
			}
			ss.mu.Lock()
			cur := ss.connection
			ss.mu.Unlock()
			return out, cur
		},
		ChooseProfile: func(name string) (string, error) {
			raw := snowflake.ListConnections(ss.connectionsFile, ss.getenv)
			found := false
			for _, p := range raw {
				if p.Name == name {
					found = true
					break
				}
			}
			if !found {
				return "", fmt.Errorf("no Snowflake connection %q in ~/.snowflake/connections.toml", name)
			}
			ss.mu.Lock()
			defer ss.mu.Unlock()
			ss.state.Profile = name
			ss.flagConnection = ""
			ss.flagWarehouse = ""
			if _, err := ss.saveStateLocked(); err != nil {
				return "", err
			}
			ss.rebuildLocked()
			return "switched to Snowflake connection " + name, nil
		},
		SaveNote:       "Saved for standalone Snowflake scratch (~/.config/xf/tmp/snowflake/connection.json).",
		ConfigKey:      config.KeySnowflakeWarehouse,
		CatalogName:    "Snowflake",
		WarehouseLabel: "virtual warehouse",
		PaletteCommand: "Snowflake: choose the virtual warehouse",
		AuthHint:       snowflake.AuthHint,
	}
}
