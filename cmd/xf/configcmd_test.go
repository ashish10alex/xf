package main

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/ashish10alex/xf/internal/config"
	"github.com/ashish10alex/xf/internal/gcpauth"
	"github.com/ashish10alex/xf/internal/testutil"
)

func TestPrintConfigJSON(t *testing.T) {
	root := t.TempDir()
	userDir := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", userDir)
	userFile := filepath.Join(userDir, "xf", "config.json")
	if err := os.MkdirAll(filepath.Dir(userFile), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(userFile, []byte(`{"editor": "nvim", "edtor": "x"}`), 0o644); err != nil {
		t.Fatal(err)
	}
	fs, _, sf, _ := tuiFlagSet()
	if help, err := parseArgs(fs, nil); help || err != nil {
		t.Fatal(help, err)
	}
	settings, err := sf.load(root)
	if err != nil {
		t.Fatal(err)
	}

	// Without --auth: what xf config prints, and no more.
	var buf bytes.Buffer
	if err := printConfigJSON(&buf, settings, root, nil); err != nil {
		t.Fatal(err)
	}
	var got map[string]json.RawMessage
	if err := json.Unmarshal(buf.Bytes(), &got); err != nil {
		t.Fatalf("%v:\n%s", err, buf.String())
	}
	if _, ok := got["googleCloud"]; ok || len(got) != 4 {
		t.Errorf("keys without --auth:\n%s", buf.String())
	}
	var out configJSON
	if err := json.Unmarshal(buf.Bytes(), &out); err != nil {
		t.Fatal(err)
	}
	if out.Project != root || len(out.ConfigFiles) != 2 || out.ConfigFiles[0] != (configFileJSON{Path: userFile, Found: true}) ||
		out.ConfigFiles[1] != (configFileJSON{Path: filepath.Join(root, config.ProjectFile)}) {
		t.Errorf("project %q, files %+v", out.Project, out.ConfigFiles)
	}
	if len(out.Settings) != len(config.Keys) || len(out.Warnings) != 1 {
		t.Errorf("%d settings, warnings %q", len(out.Settings), out.Warnings)
	}
	for _, s := range out.Settings {
		if want := settingRows(settings, root, []string{s.Key})[0]; s.Value != want.value || s.From != want.from {
			t.Errorf("%s = %q from %q, but xf config has %q from %q", s.Key, s.Value, s.From, want.value, want.from)
		}
	}

	// With --auth: both accounts, under the names the README gives. One
	// that failed says why, and has no error otherwise.
	cloud := &googleCloud{
		ADC:    gcpauth.ADC{Account: "me@work.example", Type: "authorized_user", Source: "/creds.json", SourceFrom: "$" + gcpauth.EnvFile, QuotaProject: "sandbox"},
		Gcloud: gcpauth.CLI{Error: "gcloud not found on PATH"},
	}
	buf.Reset()
	if err := printConfigJSON(&buf, settings, root, cloud); err != nil {
		t.Fatal(err)
	}
	var withAuth struct {
		GoogleCloud map[string]map[string]string `json:"googleCloud"`
	}
	if err := json.Unmarshal(buf.Bytes(), &withAuth); err != nil {
		t.Fatal(err)
	}
	wantADC := map[string]string{"account": "me@work.example", "type": "authorized_user", "source": "/creds.json", "sourceFrom": "$GOOGLE_APPLICATION_CREDENTIALS", "quotaProject": "sandbox"}
	wantGcloud := map[string]string{"account": "", "project": "", "error": "gcloud not found on PATH"}
	for name, want := range map[string]map[string]string{"adc": wantADC, "gcloud": wantGcloud} {
		got := withAuth.GoogleCloud[name]
		if len(got) != len(want) {
			t.Errorf("%s = %v, want %v", name, got, want)
		}
		for k, v := range want {
			if got[k] != v {
				t.Errorf("%s.%s = %q, want %q", name, k, got[k], v)
			}
		}
	}
}

func TestPrintGoogleCloud(t *testing.T) {
	var buf bytes.Buffer
	printGoogleCloud(&buf, googleCloud{
		ADC:    gcpauth.ADC{Account: "me@work.example", Type: "authorized_user", Source: "/creds.json", SourceFrom: "gcloud's default file"},
		Gcloud: gcpauth.CLI{Account: "me@home.example", Project: "my-proj"},
	}, false)
	want := `
google cloud
  xf uses  me@work.example
           user login
           /creds.json  gcloud's default file
  quota    (no quota project)
  gcloud   me@home.example  project my-proj
           differs from the account xf uses
`
	if buf.String() != want {
		t.Errorf("printed:\n%s\nwant:\n%s", buf.String(), want)
	}
}

func TestLookUpGoogleCloud(t *testing.T) {
	t.Setenv(gcpauth.EnvFile, filepath.Join(t.TempDir(), "missing.json"))
	t.Setenv("FAKE_GCLOUD_CONFIG", `{"core": {"account": "me@home.example"}}`)
	got := lookUpGoogleCloud(context.Background(), gcpauth.Lookup{GcloudBin: testutil.FakeCLI(t, "gcloud")})
	if got.ADC.Error == "" || got.Gcloud != (gcpauth.CLI{Account: "me@home.example"}) {
		t.Errorf("looked up %+v", got)
	}
}
