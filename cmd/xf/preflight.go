package main

import (
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"github.com/ashish10alex/xf/internal/backend"
	"github.com/ashish10alex/xf/internal/backend/dataform"
	"github.com/ashish10alex/xf/internal/bigquery"
	"github.com/ashish10alex/xf/internal/check"
	"github.com/ashish10alex/xf/internal/config"
	"github.com/ashish10alex/xf/internal/forge"
	"github.com/ashish10alex/xf/internal/gcpauth"
	"github.com/ashish10alex/xf/internal/lineage"
)

// preflightTimeout bounds the probes: they take seconds when all is well.
const preflightTimeout = 45 * time.Second

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
		c, err := bigquery.NewClient(ctx)
		if err != nil {
			return err
		}
		_, err = c.DryRun(ctx, project, location, "SELECT 1")
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
	if pr != nil && pr.Token != "" {
		p.Comments = forge.NewComments(pr.Repo, pr.Number, pr.Token, nil)
	}
	return p
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
