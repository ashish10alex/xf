package config

import (
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"strings"
	"testing"

	"github.com/ashish10alex/xf/internal/backend/dataform"
	"github.com/ashish10alex/xf/internal/backend/dbt"
)

type fixture struct {
	home, userDir, root string
}

func newFixture(t *testing.T) fixture {
	t.Helper()
	dir := t.TempDir()
	f := fixture{home: filepath.Join(dir, "home"), root: filepath.Join(dir, "home", "work", "shop")}
	f.userDir = filepath.Join(f.home, ".config", "xf")
	return f
}

func (f fixture) write(t *testing.T, path, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

func (f fixture) load(project *string) (Loaded, error) {
	return Load(Options{Root: f.root, Project: project, UserDir: f.userDir, Home: f.home})
}

func TestProjectWinsOverUser(t *testing.T) {
	f := newFixture(t)
	f.write(t, filepath.Join(f.userDir, "config.json"), `{"runMode": "api", "dataformBin": "~/bin/dataform", "dataformLocation": "us-central1"}`)
	f.write(t, filepath.Join(f.root, ".xf", "config.json"), `{
  "dataformLocation": "europe-west2",
  "dataformRepo": "my_repo"
}`)
	l, err := f.load(nil)
	if err != nil {
		t.Fatal(err)
	}
	want := Settings{
		DataformBin:      filepath.Join(f.home, "bin", "dataform"),
		RunMode:          "api",
		DataformLocation: "europe-west2",
		DataformRepo:     "my_repo",
	}
	if l.Settings != want {
		t.Errorf("settings = %+v, want %+v", l.Settings, want)
	}
	userShown := filepath.Join("~", ".config", "xf", "config.json")
	wantSources := map[string]string{
		KeyDataformBin: userShown, KeyRunMode: userShown,
		KeyDataformLocation: ProjectFile, KeyDataformRepo: ProjectFile,
	}
	if !reflect.DeepEqual(l.Sources, wantSources) {
		t.Errorf("sources = %v, want %v", l.Sources, wantSources)
	}
	if want := []File{
		{Path: userShown, Abs: filepath.Join(f.userDir, "config.json"), Found: true},
		{Path: ProjectFile, Abs: filepath.Join(f.root, ProjectFile), Found: true},
	}; !reflect.DeepEqual(l.Files, want) {
		t.Errorf("files = %+v, want %+v", l.Files, want)
	}

	l.Set(KeyRunMode, "cli", "--run-mode")
	if l.RunMode != "cli" || l.Sources[KeyRunMode] != "--run-mode" {
		t.Errorf("after Set: %q from %q", l.RunMode, l.Sources[KeyRunMode])
	}
}

func TestNoFiles(t *testing.T) {
	f := newFixture(t)
	l, err := f.load(nil)
	if err != nil {
		t.Fatal(err)
	}
	if l.Settings != (Settings{}) || len(l.Sources) != 0 || len(l.Files) != 2 || l.Files[0].Found || l.Files[1].Found {
		t.Errorf("loaded = %+v", l)
	}
}

func TestRelativeBinIsRelativeToTheProjectRoot(t *testing.T) {
	f := newFixture(t)
	f.write(t, filepath.Join(f.root, ".xf", "config.json"), `{"dataformBin": "node_modules/.bin/dataform"}`)
	l, err := f.load(nil)
	if err != nil {
		t.Fatal(err)
	}
	if want := filepath.Join(f.root, "node_modules", ".bin", "dataform"); l.DataformBin != want {
		t.Errorf("dataformBin = %q, want %q", l.DataformBin, want)
	}
	// A bare name is looked up on PATH as usual.
	f.write(t, filepath.Join(f.root, ".xf", "config.json"), `{"dataformBin": "dataform-3"}`)
	if l, _ := f.load(nil); l.DataformBin != "dataform-3" {
		t.Errorf("dataformBin = %q, want dataform-3", l.DataformBin)
	}
}

func TestConfigFlag(t *testing.T) {
	f := newFixture(t)
	f.write(t, filepath.Join(f.root, ".xf", "config.json"), `{"dataformRepo": "project-file"}`)
	f.write(t, filepath.Join(f.userDir, "config.json"), `{"runMode": "api"}`)
	prod := filepath.Join(f.home, "configs", "prod.json")
	f.write(t, prod, `{"dataformRepo": "prod", "dataformBin": "bin/dataform"}`)

	l, err := f.load(&prod)
	if err != nil {
		t.Fatal(err)
	}
	want := Settings{RunMode: "api", DataformRepo: "prod", DataformBin: filepath.Join(f.home, "configs", "bin", "dataform")}
	if l.Settings != want {
		t.Errorf("--config: settings = %+v, want %+v", l.Settings, want)
	}

	none := ""
	if l, _ := f.load(&none); l.DataformRepo != "" || l.RunMode != "api" {
		t.Errorf("--config '': settings = %+v, want only the user file", l.Settings)
	}

	missing := filepath.Join(f.home, "nope.json")
	if _, err := f.load(&missing); !errors.Is(err, os.ErrNotExist) {
		t.Errorf("missing --config: err = %v", err)
	}
}

func TestUnknownKeysWarn(t *testing.T) {
	f := newFixture(t)
	f.write(t, filepath.Join(f.root, ".xf", "config.json"), "{\n  \"dataformRepo\": \"shop\",\n  \"dataformLocaton\": \"europe-west2\"\n}\n")
	l, err := f.load(nil)
	if err != nil {
		t.Fatal(err)
	}
	if l.DataformRepo != "shop" || len(l.Warnings) != 1 ||
		!strings.HasPrefix(l.Warnings[0], filepath.Join(".xf", "config.json")+`:3: unknown key "dataformLocaton"`) {
		t.Errorf("warnings = %q, settings %+v", l.Warnings, l.Settings)
	}
}

func TestBadConfigNamesFileAndLine(t *testing.T) {
	for _, tt := range []struct{ name, content, want string }{
		{"bad run mode", "{\n  \"dataformRepo\": \"shop\",\n  \"runMode\": \"remote\"\n}", `.xf/config.json:3: runMode must be "cli" or "api", not "remote"`},
		{"not a string", "{\n  \"dataformRepo\": 42\n}", `.xf/config.json:2: dataformRepo must be a string, not 42`},
		{"trailing comma", "{\n  \"runMode\": \"api\",\n}", `.xf/config.json:3:`},
		{"not an object", `["api"]`, `.xf/config.json:1: want a JSON object`},
		{"truncated", "{\n  \"runMode\": \"api\"", `.xf/config.json:2: unexpected end of file`},
		{"two objects", `{} {}`, `.xf/config.json:1: unexpected content after the object`},
		{"autosave not a number", "{\n  \"sourceAutosaveMs\": \"soon\"\n}", `.xf/config.json:2: sourceAutosaveMs must be a whole number of milliseconds, 0 or more, not "soon"`},
		{"autosave negative", "{\n  \"sourceAutosaveMs\": -5\n}", `.xf/config.json:2: sourceAutosaveMs must be a whole number of milliseconds, 0 or more, not -5`},
		{"autosave fraction", "{\n  \"sourceAutosaveMs\": 1.5\n}", `.xf/config.json:2: sourceAutosaveMs must be a whole number`},
	} {
		f := newFixture(t)
		f.write(t, filepath.Join(f.root, ".xf", "config.json"), tt.content)
		_, err := f.load(nil)
		want := strings.Replace(tt.want, ".xf/config.json", filepath.Join(".xf", "config.json"), 1)
		if err == nil || !strings.HasPrefix(err.Error(), want) {
			t.Errorf("%s: err = %v, want prefix %q", tt.name, err, want)
		}
	}
}

func TestUserDir(t *testing.T) {
	defer func(g string) { goos = g }(goos)
	home := filepath.FromSlash("/home/me")
	for _, tt := range []struct {
		goos string
		env  map[string]string
		want string
	}{
		{"linux", map[string]string{"XDG_CONFIG_HOME": "/xdg"}, "/xdg/xf"},
		{"darwin", nil, "/home/me/.config/xf"},
		{"windows", map[string]string{"AppData": "/users/me/AppData/Roaming"}, "/users/me/AppData/Roaming/xf"},
		{"windows", map[string]string{"XDG_CONFIG_HOME": "/xdg", "AppData": "/users/me/AppData/Roaming"}, "/xdg/xf"},
		{"windows", nil, "/home/me/.config/xf"},
	} {
		goos = tt.goos
		got := UserDir(func(k string) string { return filepath.FromSlash(tt.env[k]) }, home)
		if got != filepath.FromSlash(tt.want) {
			t.Errorf("%s %v: %s, want %s", tt.goos, tt.env, got, filepath.FromSlash(tt.want))
		}
	}
}

func TestWrite(t *testing.T) {
	f := newFixture(t)
	s := Settings{DataformRepo: "my_repo", DataformLocation: "europe-west2"}
	path, err := Write(f.root, s, false)
	if err != nil {
		t.Fatal(err)
	}
	data, _ := os.ReadFile(path)
	want := "{\n  \"dataformLocation\": \"europe-west2\",\n  \"dataformRepo\": \"my_repo\"\n}\n"
	if string(data) != want {
		t.Errorf("wrote:\n%s\nwant:\n%s", data, want)
	}
	if l, err := f.load(nil); err != nil || l.Settings != s {
		t.Errorf("read back %+v, %v", l.Settings, err)
	}
	if _, err := Write(f.root, Settings{DataformRepo: "x"}, false); !errors.Is(err, os.ErrExist) {
		t.Errorf("second write: err = %v, want ErrExist", err)
	}
	if _, err := Write(f.root, Settings{DataformRepo: "x"}, true); err != nil {
		t.Errorf("forced write: %v", err)
	}
}

func TestSetProjectKeyKeepsTheOtherKeysAndWritesAnEmptyValue(t *testing.T) {
	f := newFixture(t)
	// No file yet: it is created.
	path, err := SetProjectKey(f.root, KeyProdCompilerOptions, "--default-database=acme-prod")
	if err != nil {
		t.Fatal(err)
	}
	if data, _ := os.ReadFile(path); string(data) != "{\n  \"prodCompilerOptions\": \"--default-database=acme-prod\"\n}\n" {
		t.Errorf("created:\n%s", data)
	}

	// A hand-written file: its keys stay, in the documented order, with
	// the ones xf doesn't know after them.
	f.write(t, path, "{\n   \"editor\": \"nvim\",\n \"mine\": {\"a\": 1},\n  \"dbtCompileWithHooks\": true,\n\"dataformRepo\": \"shop\"}")
	if _, err := SetProjectKey(f.root, KeyProdCompilerOptions, ""); err != nil {
		t.Fatal(err)
	}
	data, _ := os.ReadFile(path)
	want := "{\n  \"dbtCompileWithHooks\": true,\n  \"dataformRepo\": \"shop\",\n  \"prodCompilerOptions\": \"\",\n  \"editor\": \"nvim\",\n  \"mine\": {\"a\": 1}\n}\n"
	if string(data) != want {
		t.Errorf("wrote:\n%s\nwant:\n%s", data, want)
	}
	// "" is set, which is what stops xf asking.
	l, err := f.load(nil)
	if err != nil || !l.IsSet(KeyProdCompilerOptions) || l.ProdCompilerOptions != "" || l.IsSet(KeyDevCompilerOptions) || l.Editor != "nvim" {
		t.Errorf("read back %+v, %v", l, err)
	}

	f.write(t, path, "[]")
	if _, err := SetProjectKey(f.root, KeyProdCompilerOptions, "x"); err == nil {
		t.Error("a file that isn't an object was replaced")
	}
	if _, err := SetProjectKey(f.root, "nope", "x"); err == nil {
		t.Error("an unknown key was written")
	}
}

func TestSetProjectBranchDropsTheKeyWhenItIsInherited(t *testing.T) {
	root := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	keys := func() map[string]string {
		k, err := ReadKeys(filepath.Join(root, ProjectFile))
		if err != nil {
			t.Fatal(err)
		}
		return k
	}
	if _, err := SetProjectBranch(root, "develop"); err != nil {
		t.Fatal(err)
	}
	if got := keys()[KeyDefaultBranch]; got != "develop" {
		t.Errorf("set develop: the file says %q", got)
	}
	// main is the default: the key comes out, so the default applies.
	if _, err := SetProjectBranch(root, DefaultBranch); err != nil {
		t.Fatal(err)
	}
	if got, ok := keys()[KeyDefaultBranch]; ok {
		t.Errorf("set main, the default: the file still says %q", got)
	}
	// The user's file says trunk: main is set, trunk inherited.
	if _, err := SetUserKey(KeyDefaultBranch, "trunk"); err != nil {
		t.Fatal(err)
	}
	if _, err := SetProjectBranch(root, DefaultBranch); err != nil || keys()[KeyDefaultBranch] != DefaultBranch {
		t.Errorf("set main over the user's trunk: %v, the file says %q", err, keys()[KeyDefaultBranch])
	}
	if _, err := SetProjectBranch(root, "trunk"); err != nil {
		t.Fatal(err)
	}
	if got, ok := keys()[KeyDefaultBranch]; ok {
		t.Errorf("set trunk, the user's: the file still says %q", got)
	}
	if _, err := SetProjectBranch(root, ""); err == nil {
		t.Error("an empty branch was taken")
	}
}

func TestSetUserKey(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", dir)
	want := filepath.Join(dir, "xf", "config.json")
	if err := os.MkdirAll(filepath.Dir(want), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(want, []byte(`{"defaultBranch": "develop"}`), 0o644); err != nil {
		t.Fatal(err)
	}
	path, err := SetUserKey(KeyEditor, "zed")
	if err != nil || path != want {
		t.Fatalf("SetUserKey = %q, %v; want %q", path, err, want)
	}
	data, _ := os.ReadFile(path)
	if got := string(data); !strings.Contains(got, `"editor": "zed"`) || !strings.Contains(got, `"defaultBranch": "develop"`) {
		t.Errorf("file = %s, want the editor added and the branch kept", got)
	}
}

func TestThemeIsTheUsers(t *testing.T) {
	f := newFixture(t)
	f.write(t, filepath.Join(f.userDir, "config.json"), `{"theme": "dark"}`)
	f.write(t, filepath.Join(f.root, ProjectFile), `{"theme": "light", "runMode": "api"}`)
	l, err := f.load(nil)
	if err != nil {
		t.Fatal(err)
	}
	if l.Theme != "dark" || l.RunMode != "api" || len(l.Warnings) != 1 || !strings.Contains(l.Warnings[0], "theme is ignored") {
		t.Errorf("theme %q, runMode %q, warnings %q", l.Theme, l.RunMode, l.Warnings)
	}

	// A project's theme with none of the user's leaves it unset.
	f.write(t, filepath.Join(f.userDir, "config.json"), `{}`)
	if l, err = f.load(nil); err != nil || l.Theme != "" || l.IsSet(KeyTheme) {
		t.Errorf("theme %q set %v, %v", l.Theme, l.IsSet(KeyTheme), err)
	}

	f.write(t, filepath.Join(f.userDir, "config.json"), `{"theme": "sepia"}`)
	if _, err = f.load(nil); err == nil || !strings.Contains(err.Error(), "must be") {
		t.Errorf("a bad theme: %v", err)
	}
}

func TestWebThemeIsTheProjectsToo(t *testing.T) {
	f := newFixture(t)
	f.write(t, filepath.Join(f.userDir, "config.json"), `{"webTheme": "light", "webThemeDark": "nord"}`)
	l, err := f.load(nil)
	if err != nil {
		t.Fatal(err)
	}
	if l.WebTheme != "light" || l.WebThemeDark != "nord" || l.Theme != "" || len(l.Warnings) != 0 {
		t.Errorf("webTheme %q, dark %q, theme %q, warnings %q", l.WebTheme, l.WebThemeDark, l.Theme, l.Warnings)
	}
	// A project's file wins over the user's for the keys it sets, and
	// leaves the others.
	f.write(t, filepath.Join(f.root, ProjectFile), `{"webTheme": "dracula", "theme": "dark"}`)
	l, err = f.load(nil)
	if err != nil {
		t.Fatal(err)
	}
	if l.WebTheme != "dracula" || l.WebThemeDark != "nord" || l.Theme != "" || len(l.Warnings) != 1 || !strings.Contains(l.Warnings[0], "theme is ignored") {
		t.Errorf("webTheme %q, dark %q, theme %q, warnings %q", l.WebTheme, l.WebThemeDark, l.Theme, l.Warnings)
	}
	f.write(t, filepath.Join(f.root, ProjectFile), `{"webTheme": "sepia"}`)
	if _, err = f.load(nil); err == nil || !strings.Contains(err.Error(), "must be") {
		t.Errorf("a bad theme: %v", err)
	}
}

func TestWebAutoExitSecondsIsTheUsers(t *testing.T) {
	f := newFixture(t)
	f.write(t, filepath.Join(f.userDir, "config.json"), `{"webAutoExitSeconds": 90}`)
	f.write(t, filepath.Join(f.root, ProjectFile), `{"webAutoExitSeconds": "0"}`)
	l, err := f.load(nil)
	if err != nil {
		t.Fatal(err)
	}
	if l.WebAutoExitSeconds != "90" || len(l.Warnings) != 1 || !strings.Contains(l.Warnings[0], "webAutoExitSeconds is ignored") {
		t.Errorf("webAutoExitSeconds %q, warnings %q", l.WebAutoExitSeconds, l.Warnings)
	}
	for _, bad := range []string{`-1`, `"soon"`, `1.5`} {
		f.write(t, filepath.Join(f.userDir, "config.json"), `{"webAutoExitSeconds": `+bad+`}`)
		if _, err = f.load(nil); err == nil || !strings.Contains(err.Error(), "whole number of seconds") {
			t.Errorf("webAutoExitSeconds %s: %v", bad, err)
		}
	}
	if !UserOnly(KeyWebAutoExitSeconds) {
		t.Error("webAutoExitSeconds isn't user-only")
	}
	if !UserOnly(KeyWebOpenProjectIn) {
		t.Error("webOpenProjectIn isn't user-only")
	}
	if !UserOnly(KeyPane2Tab) {
		t.Error("pane2Tab isn't user-only")
	}
	if !UserOnly(KeyWrapLines) {
		t.Error("wrapLines isn't user-only")
	}
}

func TestVimKeysAreTheUsers(t *testing.T) {
	f := newFixture(t)
	f.write(t, filepath.Join(f.userDir, "config.json"), `{"vimNextHunk": "]c"}`)
	f.write(t, filepath.Join(f.root, ProjectFile), `{"vimNextHunk": "]h", "vimHover": "gh"}`)
	l, err := f.load(nil)
	if err != nil {
		t.Fatal(err)
	}
	if got := []string{l.VimKey(KeyVimNextHunk), l.VimKey(KeyVimHover), l.VimKey(KeyVimPrevHunk)}; !reflect.DeepEqual(got, []string{"]c", "K", "[g"}) {
		t.Errorf("vim keys %q, want the user's ]c and the defaults", got)
	}
	if len(l.Warnings) != 2 {
		t.Errorf("warnings %q, want one per vim key the project sets", l.Warnings)
	}
	for _, k := range VimKeys {
		if !UserOnly(k) || (VimKeyDefaults[k] == "") != (k == KeyVimFormat) || Flags[k] != "" {
			t.Errorf("%s: user-only %v, default %q, flag %q", k, UserOnly(k), VimKeyDefaults[k], Flags[k])
		}
	}
}

func TestNextTheme(t *testing.T) {
	got := []string{NextTheme(""), NextTheme(ThemeAuto), NextTheme(ThemeLight), NextTheme(ThemeDark)}
	if want := []string{"light", "light", "dark", "auto"}; !reflect.DeepEqual(got, want) {
		t.Errorf("got %q, want %q", got, want)
	}
}

func TestSourceAutosaveMsIsANumber(t *testing.T) {
	for _, content := range []string{`{"sourceAutosaveMs": 1500}`, `{"sourceAutosaveMs": "1500"}`} {
		f := newFixture(t)
		f.write(t, filepath.Join(f.root, ".xf", "config.json"), content)
		l, err := f.load(nil)
		if err != nil {
			t.Fatal(err)
		}
		if got := l.SourceAutosaveMs; got != "1500" {
			t.Errorf("%s: SourceAutosaveMs = %q, want 1500", content, got)
		}
	}
}

func TestCheck(t *testing.T) {
	for _, tt := range []struct {
		key, value string
		ok         bool
	}{
		{KeySourceAutosaveMs, "1500", true}, {KeySourceAutosaveMs, "soon", false}, {KeySourceAutosaveMs, "-1", false},
		{KeyScratchWarnGB, "2.5", true}, {KeyScratchWarnGB, "0", false},
		{KeyDbtCompileWithHooks, "true", true}, {KeyDbtCompileWithHooks, "yes", false},
		{KeyRunMode, "api", true}, {KeyRunMode, "remote", false},
		{KeyWebTheme, "dark", true}, {KeyWebTheme, "blue", false}, {KeyWebTheme, "dracula", true}, {KeyWebTheme, "auto", true},
		{KeyTheme, "dracula", false},
		{KeyWebThemeLight, "catppuccin-latte", true}, {KeyWebThemeDark, "light", true}, {KeyWebThemeDark, "auto", false}, {KeyWebThemeLight, "", false},
		{KeyWebAutoExitSeconds, "0", true}, {KeyWebAutoExitSeconds, "45", true}, {KeyWebAutoExitSeconds, "-5", false},
		{KeyWebOpenProjectIn, "same-tab", true}, {KeyWebOpenProjectIn, "new-tab", true}, {KeyWebOpenProjectIn, "window", false},
		{KeyPane2Tab, "sql", true}, {KeyPane2Tab, "columns", true}, {KeyPane2Tab, "lineage", false},
		{KeyWrapLines, "true", true}, {KeyWrapLines, "false", true}, {KeyWrapLines, "on", false},
		{KeyEditor, "code", true}, {"nope", "x", false},
		{KeyVimNextHunk, "]c", true}, {KeyVimHover, "<C-k>", true}, {KeyVimNextHunk, "", false}, {KeyVimNextHunk, "] g", false},
	} {
		if err := Check(tt.key, tt.value); (err == nil) != tt.ok {
			t.Errorf("Check(%s, %q) = %v, want ok %v", tt.key, tt.value, err, tt.ok)
		}
	}
}

func TestSetAndUnsetWriteTypedValues(t *testing.T) {
	f := newFixture(t)
	if err := os.MkdirAll(f.root, 0o755); err != nil {
		t.Fatal(err)
	}
	for _, kv := range [][2]string{{KeySourceAutosaveMs, "1500"}, {KeyDbtCompileWithHooks, "true"}, {KeyEditor, "code"}, {KeyProdCompilerOptions, ""}} {
		if _, err := SetProjectKey(f.root, kv[0], kv[1]); err != nil {
			t.Fatal(err)
		}
	}
	path := filepath.Join(f.root, ProjectFile)
	data, _ := os.ReadFile(path)
	want := "{\n  \"dbtCompileWithHooks\": true,\n  \"prodCompilerOptions\": \"\",\n  \"editor\": \"code\",\n  \"sourceAutosaveMs\": 1500\n}\n"
	if string(data) != want {
		t.Errorf("file =\n%s\nwant\n%s", data, want)
	}
	l, err := f.load(nil)
	if err != nil {
		t.Fatal(err)
	}
	if l.SourceAutosaveMs != "1500" || l.DbtCompileWithHooks != "true" || !l.IsSet(KeyProdCompilerOptions) {
		t.Errorf("loaded %+v", l.Settings)
	}
	if _, err := UnsetProjectKey(f.root, KeySourceAutosaveMs); err != nil {
		t.Fatal(err)
	}
	if l, _ := f.load(nil); l.IsSet(KeySourceAutosaveMs) || !l.IsSet(KeyEditor) {
		t.Errorf("after unset: %+v", l.Settings)
	}
	// Unsetting in a project with no file makes none.
	other := t.TempDir()
	if _, err := UnsetProjectKey(other, KeyEditor); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(other, ProjectFile)); err == nil {
		t.Error("unset made a file")
	}
}

// Every key has a title and a default, so a new key can't ship without
// them; the defaults that name an environment variable name the one the
// tools read.
func TestEveryKeyHasATitleAndADefault(t *testing.T) {
	for _, k := range Keys {
		if strings.TrimSpace(Titles[k]) == "" {
			t.Errorf("%s has no title", k)
		}
		if strings.TrimSpace(Defaults[k]) == "" {
			t.Errorf("%s has no default (NoDefault when it has none)", k)
		}
	}
	for k := range Titles {
		if !slices.Contains(Keys, k) {
			t.Errorf("title of unknown key %s", k)
		}
	}
	for k := range Defaults {
		if !slices.Contains(Keys, k) {
			t.Errorf("default of unknown key %s", k)
		}
	}
	if !strings.Contains(Defaults[KeyDataformBin], "$"+dataform.BinEnv) || !strings.Contains(Defaults[KeyDbtBin], "$"+dbt.BinEnv) {
		t.Errorf("binaries' defaults %q, %q don't name $%s and $%s", Defaults[KeyDataformBin], Defaults[KeyDbtBin], dataform.BinEnv, dbt.BinEnv)
	}
}

func TestCheckFile(t *testing.T) {
	for _, tt := range []struct {
		name, text string
		user       bool
		errs       []Problem // Message checked as a substring
		warns      []Problem
	}{
		{name: "good", text: "{\n  \"editor\": \"nvim\",\n  \"sourceAutosaveMs\": 1500\n}\n", user: true},
		{name: "empty object", text: "{}", user: false},
		{name: "broken", text: "{\n  \"editor\": \"nvim\"\n  \"theme\": \"dark\"\n}\n", user: true,
			errs: []Problem{{Line: 3, Message: "invalid character"}}},
		{name: "cut short", text: "{\n  \"editor\": \"nvim\",\n", user: true,
			errs: []Problem{{Line: 3, Message: "unexpected end of file"}}},
		{name: "not an object", text: "[1]", user: true, errs: []Problem{{Line: 1, Message: "want a JSON object"}}},
		{name: "bad value", text: "{\n  \"editor\": \"nvim\",\n  \"sourceAutosaveMs\": \"soon\"\n}", user: true,
			errs: []Problem{{Line: 3, Key: KeySourceAutosaveMs, Message: "whole number"}}},
		{name: "unknown key", text: "{\n  \"dataformLocaton\": \"eu\"\n}", user: false,
			warns: []Problem{{Line: 2, Key: "dataformLocaton", Message: "unknown key \"dataformLocaton\" (known: dataformBin"}}},
		{name: "user-only key in a project's file", text: "{\n  \"theme\": \"dark\"\n}", user: false,
			errs: []Problem{{Line: 2, Key: KeyTheme, Message: "user config only"}}},
		{name: "user-only key in the user's file", text: "{\"theme\": \"dark\"}", user: true},
		{name: "web theme in a project's file", text: "{\"webTheme\": \"dracula\"}", user: false},
		{name: "vim keys taken", text: "{\n  \"vimNextHunk\": \"K\"\n}", user: true,
			errs: []Problem{{Line: 2, Key: KeyVimNextHunk, Message: "vimHover already has K"}}},
		{name: "a leader that makes two the same", text: "{\n  \"vimLeader\": \"g\",\n  \"vimBuffers\": \"<Leader>d\"\n}", user: true,
			errs: []Problem{{Line: 2, Key: KeyVimLeader, Message: "would both be gd"}, {Line: 3, Key: KeyVimBuffers, Message: "vimDefinition already has gd"}}},
		{name: "a leader of its own", text: "{\n  \"vimLeader\": \",\",\n  \"vimBuffers\": \"<leader>d\"\n}", user: true},
		{name: "vim keys swapped", text: "{\"vimNextHunk\": \"K\", \"vimHover\": \"]g\"}", user: true},
	} {
		t.Run(tt.name, func(t *testing.T) {
			errs, warns := CheckFile([]byte(tt.text), tt.user)
			match := func(what string, got, want []Problem) {
				if len(got) != len(want) {
					t.Fatalf("%s = %+v, want %+v", what, got, want)
				}
				for i := range want {
					if got[i].Line != want[i].Line || got[i].Key != want[i].Key || !strings.Contains(got[i].Message, want[i].Message) {
						t.Errorf("%s[%d] = %+v, want %+v", what, i, got[i], want[i])
					}
				}
			}
			match("errors", errs, tt.errs)
			match("warnings", warns, tt.warns)
		})
	}
}
