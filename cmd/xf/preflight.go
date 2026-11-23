package main

import (
	"cmp"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"time"

	"github.com/ashish10alex/xf/internal/backend"
	"github.com/ashish10alex/xf/internal/backend/dataform"
	"github.com/ashish10alex/xf/internal/check"
	"github.com/ashish10alex/xf/internal/config"
	"github.com/ashish10alex/xf/internal/databricks"
	"github.com/ashish10alex/xf/internal/forge"
	"github.com/ashish10alex/xf/internal/gcpauth"
	"github.com/ashish10alex/xf/internal/lineage"
	"github.com/ashish10alex/xf/internal/project"
	"github.com/ashish10alex/xf/internal/snowflake"
	"github.com/ashish10alex/xf/internal/warehouse"
)

// preflightTimeout bounds the probes: they take seconds when all is well.
const preflightTimeout = 45 * time.Second

var (
	validPreflightProjectRE = regexp.MustCompile(`^[A-Za-z0-9_.:-]+$`)
	validPreflightRegionRE  = regexp.MustCompile(`^[a-z0-9-]+$`)
)

// inCI says xf runs in a CI job, where the probes run as CI's identity.
func inCI(getenv func(string) string) bool {
	return getenv("CI") == "true" || getenv("GITHUB_ACTIONS") == "true" || getenv("GITLAB_CI") == "true"
}

// preflightProbes are the probes for the project at root (ADR 0041). pr and
// prErr are FindPR's, when xf check comments.
func preflightProbes(name, root, base string, settings config.Loaded, comment bool, pr *check.PR, prErr error) check.Probes {
	p := check.Probes{
		Dbt:     name == backend.DBT,
		Base:    "origin/" + base,
		Git:     func(args ...string) (string, error) { return gitOut(root, args...) },
		ADC:     func(ctx context.Context) gcpauth.ADC { return gcpauth.Lookup{}.ADC(ctx) },
		AsYou:   !inCI(os.Getenv),
		Comment: comment,
		PR:      pr,
		PRErr:   prErr,
	}
	if p.Dbt {
		p.Bin, p.BinErr = resolveDbtBin(settings, root)
		p.Prod = prodSetting(root, settings, config.KeyDbtProdTarget, unsetDbtProdTarget(settings, os.Getenv) == "")
	} else {
		p.Bin, p.BinErr = resolveBin(settings)
		p.CoreNeeded, p.CoreVersion = dataformCore(root)
		p.Prod = prodSetting(root, settings, config.KeyProdCompilerOptions, settings.IsSet(config.KeyProdCompilerOptions))
		p.DefaultProject, p.DefaultUnknown = defaultProject(root, settings)
		p.ProdProject, p.ProdUnknown = prodProject(settings.ProdCompilerOptions)
		p.Location = dataform.ReadWorkflowSetting(root, "defaultLocation")
	}
	p.DryRun = func(ctx context.Context, project, location string) error {
		c, err := newWarehouse(ctx)
		if err != nil {
			return err
		}
		_, err = c.Check(ctx, project, location, "SELECT 1")
		return err
	}
	p.Lineage = func(ctx context.Context, project, location string) error {
		c, err := lineage.NewClient(ctx)
		if err != nil {
			return err
		}
		_, err = c.SearchLinks(ctx, lineage.SearchRequest{Parent: "projects/" + project + "/locations/" + location,
			Target: &lineage.EntityReference{FullyQualifiedName: lineage.FQN(project + ".xf_preflight.xf_preflight")}})
		return err
	}
	if qhProjects := config.ParseLineageProjects(settings.QueryHistoryProjects); len(qhProjects) > 0 {
		if _, enabled := config.ParseQueryHistoryDays(settings.QueryHistoryDays); enabled {
			p.QueryHistoryProjects = qhProjects
			p.QueryHistoryExtra = config.ParseLineageProjects(settings.LineageProjects)
			p.QueryHistoryProbe = func(ctx context.Context, billingProject, viewProject, location string, orgView bool) error {
				reg := strings.ToLower(strings.TrimSpace(location))
				if !validPreflightRegionRE.MatchString(reg) {
					return fmt.Errorf("invalid BigQuery region %q", location)
				}
				if !orgView && !validPreflightProjectRE.MatchString(viewProject) {
					return fmt.Errorf("invalid BigQuery project %q", viewProject)
				}
				c, err := newWarehouse(ctx)
				if err != nil {
					return err
				}
				view := fmt.Sprintf("`%s.region-%s.INFORMATION_SCHEMA.JOBS_BY_PROJECT`", viewProject, reg)
				if orgView {
					view = fmt.Sprintf("`region-%s.INFORMATION_SCHEMA.JOBS_BY_ORGANIZATION`", reg)
				}
				_, err = c.Check(ctx, billingProject, location, "SELECT 1 FROM "+view+" LIMIT 0")
				return err
			}
		}
	}
	if pr != nil && pr.Token != "" {
		p.Comments = forge.NewComments(pr.Repo, pr.Number, pr.Token, nil)
	}
	if p.Dbt {
		pw := newProjectWarehouse(root, settings.DbtTarget, func() (config.Loaded, error) { return settings, nil })
		switch pw.kind() {
		case project.Databricks:
			p.Databricks = pw.preflight
		case project.Snowflake:
			p.Warehouse = pw.snowflakePreflight
		}
	}
	return p
}

// snowflakePreflight is xf check's probe of a Snowflake project (ADR 0044):
// its account and virtual warehouse via SHOW WAREHOUSES in Cloud Services,
// which wakes nothing.
func (w *projectWarehouse) snowflakePreflight(ctx context.Context) check.Line {
	line := check.Line{Name: "Snowflake"}
	fail := func(err error) check.Line {
		line.Status, line.Detail = check.Fail, err.Error()
		if errors.Is(err, warehouse.ErrAuth) {
			line.Fix = snowflake.AuthHint
		}
		return line
	}
	c := w.snowflake()
	acct := cmp.Or(c.Account(), c.Host())
	if c.Warehouse() == "" {
		if _, err := c.Warehouses(ctx); err != nil {
			return fail(err)
		}
		line.Status, line.Detail = check.Warn, "signed in to "+acct+", but no virtual warehouse is chosen: column impact and query cost need one"
		line.Fix = "set snowflakeWarehouse, or the dbt target's warehouse"
		return line
	}
	vw, err := c.VirtualWarehouse(ctx)
	if err != nil {
		return fail(err)
	}
	state := strings.ToLower(vw.State)
	if vw.State != "STARTED" {
		state += ": checks resume it, and it bills while it runs"
	}
	line.Status, line.Detail = check.Pass, fmt.Sprintf("virtual warehouse %s on %s (%s)", cmp.Or(vw.Name, c.Warehouse()), acct, state)
	return line
}

// preflight is xf check's probe of a Databricks project (ADR 0042): its
// SQL warehouse, as it stands, which asking doesn't start. Without one it
// warns, as query cost reads Query History and needs none; without
// credentials it fails, as both checks need them.
func (w *projectWarehouse) preflight(ctx context.Context) check.Line {
	line := check.Line{Name: "Databricks"}
	fail := func(err error) check.Line {
		line.Status, line.Detail = check.Fail, err.Error()
		if errors.Is(err, warehouse.ErrAuth) {
			line.Fix = databricks.AuthHint
		}
		return line
	}
	c := w.databricks()
	if c.Warehouse() == "" {
		if _, err := c.Warehouses(ctx); err != nil {
			return fail(err)
		}
		line.Status, line.Detail = check.Warn, "signed in to "+c.Host()+", but no SQL warehouse is chosen: column impact can't plan the changed models; query cost needs none"
		line.Fix = "set databricksWarehouse, or the dbt target's http_path"
		return line
	}
	sw, err := c.SQLWarehouse(ctx)
	if err != nil {
		return fail(err)
	}
	state := strings.ToLower(sw.State)
	if sw.State != "RUNNING" {
		state += ": column impact starts it, and it bills while it runs"
	}
	line.Status, line.Detail = check.Pass, fmt.Sprintf("SQL warehouse %s on %s (%s)", cmp.Or(sw.Name, sw.ID), c.Host(), state)
	return line
}

// runPreflight runs the probes, bounded by preflightTimeout.
func runPreflight(ctx context.Context, p check.Probes) *check.Preflight {
	ctx, cancel := context.WithTimeout(ctx, preflightTimeout)
	defer cancel()
	return check.Run(ctx, p)
}

// gitOut is git's output in root, trimmed.
func gitOut(root string, args ...string) (string, error) {
	out, err := exec.Command("git", append([]string{"-C", root}, args...)...).Output()
	return strings.TrimSpace(string(out)), err
}

// dataformCore says whether package.json at root lists @dataform/core, and
// the version node_modules has ("" for none).
func dataformCore(root string) (needed bool, version string) {
	data, err := os.ReadFile(filepath.Join(root, "package.json"))
	if err != nil {
		return false, ""
	}
	var pkg struct {
		Deps    map[string]string `json:"dependencies"`
		DevDeps map[string]string `json:"devDependencies"`
	}
	if json.Unmarshal(data, &pkg) != nil {
		return false, ""
	}
	if _, ok := pkg.Deps["@dataform/core"]; !ok {
		if _, ok := pkg.DevDeps["@dataform/core"]; !ok {
			return false, ""
		}
	}
	data, err = os.ReadFile(filepath.Join(root, "node_modules", "@dataform", "core", "package.json"))
	if err != nil {
		return true, ""
	}
	var core struct {
		Version string `json:"version"`
	}
	if json.Unmarshal(data, &core) != nil || core.Version == "" {
		return true, "installed"
	}
	return true, core.Version
}

// prodSetting is key as settings have it, and whether CI sees where it
// comes from: a flag, or a file git tracks.
func prodSetting(root string, settings config.Loaded, key string, set bool) check.Prod {
	p := check.Prod{Key: key, Set: set, Value: settings.Get(key), Source: settings.Sources[key]}
	if !set || p.Source == "" || strings.HasPrefix(p.Source, "--") {
		return p
	}
	for _, f := range settings.Files {
		if f.Path != p.Source || !f.Found {
			continue
		}
		if rel, err := filepath.Rel(root, f.Abs); err != nil || strings.HasPrefix(rel, "..") {
			p.Untracked, p.Personal = true, true
		} else if _, err := gitOut(root, "ls-files", "--error-unmatch", f.Abs); err != nil {
			p.Untracked = true
		}
	}
	return p
}

// defaultProject is the project a compile with no options dry runs in, or
// why only the compile knows it.
func defaultProject(root string, settings config.Loaded) (project, unknown string) {
	names, err := dataform.ReadWorkflowNames(root)
	switch {
	case err != nil:
		return "", "no " + dataform.SettingsFile
	case names.Project == "":
		return "", dataform.SettingsFile + " has no defaultProject"
	case names.DatabaseSuffix != "" || settings.DatabaseSuffix != "":
		return "", "a projectSuffix is added to " + names.Project
	}
	return names.Project, ""
}

// prodProject is the project prodCompilerOptions name with
// --default-database, or why only the compile knows it.
func prodProject(options string) (project, unknown string) {
	args, err := dataform.SplitOptions(options)
	if err != nil {
		return "", "prodCompilerOptions can't be read: " + err.Error()
	}
	for i, a := range args {
		if a == "--database-suffix" || strings.HasPrefix(a, "--database-suffix=") {
			return "", "prodCompilerOptions add a --database-suffix"
		}
		if v, ok := strings.CutPrefix(a, "--default-database="); ok {
			project = v
		} else if a == "--default-database" && i+1 < len(args) {
			project = args[i+1]
		}
	}
	if project == "" {
		return "", "prodCompilerOptions don't name --default-database"
	}
	return project, ""
}
