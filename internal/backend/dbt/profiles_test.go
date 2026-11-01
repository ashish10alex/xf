package dbt

import (
	"os"
	"path/filepath"
	"reflect"
	"testing"
)

const testProfiles = `# Two projects' profiles.
other:
  target: x
  outputs:
    x: {type: bigquery}

shop:
  target: dev   # the default
  outputs:
    dev:
      type: bigquery
      project: "{{ env_var('SHOP_PROJECT', 'shop-dev') }}"
      dataset: shop
      location: EU
    ci:
      type: bigquery
      project: '{{ env_var("SHOP_PROJECT") }}'
      dataset: shop_ci # per run

    prod:
      type: bigquery
      database: shop-prod
      schema: "{{ var('schema') }}"
`

func writeProject(t *testing.T, profilesDir string) string {
	t.Helper()
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, ProjectFile), []byte("name: shop\nprofile: 'shop'\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if profilesDir == "" {
		profilesDir = root
	}
	if err := os.WriteFile(filepath.Join(profilesDir, "profiles.yml"), []byte(testProfiles), 0o644); err != nil {
		t.Fatal(err)
	}
	return root
}

func TestReadProfile(t *testing.T) {
	root := writeProject(t, "")
	env := map[string]string{"SHOP_PROJECT": ""}
	p, err := ReadProfile(root, func(k string) string { return env[k] })
	if err != nil {
		t.Fatal(err)
	}
	want := Profile{Name: "shop", File: filepath.Join(root, "profiles.yml"), Default: "dev", Targets: []Target{
		{Name: "dev", Database: "shop-dev", Schema: "shop"},
		{Name: "ci", Database: "", Schema: "shop_ci"},
		{Name: "prod", Database: "shop-prod", Schema: "{{ var('schema') }}"},
	}}
	if !reflect.DeepEqual(p, want) {
		t.Errorf("profile:\n%+v\nwant:\n%+v", p, want)
	}

	env["SHOP_PROJECT"] = "shop-mine"
	p, _ = ReadProfile(root, func(k string) string { return env[k] })
	if p.Targets[0].Database != "shop-mine" || p.Targets[1].Database != "shop-mine" {
		t.Errorf("env_var not read: %+v", p.Targets)
	}
}

func TestReadProfileFromProfilesDir(t *testing.T) {
	dir := t.TempDir()
	root := writeProject(t, dir)
	p, err := ReadProfile(root, func(k string) string {
		if k == "DBT_PROFILES_DIR" {
			return dir
		}
		return ""
	})
	if err != nil || p.File != filepath.Join(dir, "profiles.yml") || len(p.Targets) != 3 {
		t.Errorf("ReadProfile = %+v, %v", p, err)
	}
}

func TestReadProfileMissing(t *testing.T) {
	root := writeProject(t, "")
	if err := os.WriteFile(filepath.Join(root, ProjectFile), []byte("profile: nope\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := ReadProfile(root, func(string) string { return "" }); err == nil {
		t.Error("no error for a profile profiles.yml doesn't have")
	}
}
