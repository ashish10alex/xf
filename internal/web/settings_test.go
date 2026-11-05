package web

import (
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"testing"

	"github.com/ashish10alex/xf/internal/config"
	"github.com/ashish10alex/xf/internal/frontend"
	"github.com/ashish10alex/xf/internal/session"
)

// fakeSettings keeps the two files' keys in memory; a project value wins
// over the user's, as in the files.
type fakeSettings struct {
	mu            sync.Mutex
	user, project map[string]string
	// dir holds the files the JSON view reads and writes whole, user.json
	// and project.json, whose keys the view shows over the maps'.
	dir string
}

// fileKeys are the keys level's file in f.dir sets.
func (f *fakeSettings) fileKeys(level string) map[string]string {
	if f.dir == "" {
		return nil
	}
	m, _ := config.ReadKeys(filepath.Join(f.dir, level+".json"))
	return m
}

func (f *fakeSettings) hook() *Settings {
	return &Settings{
		View: func(root, tool string) SettingsView {
			f.mu.Lock()
			defer f.mu.Unlock()
			v := SettingsView{Levels: []SettingsLevel{{Level: "user", Path: "~/.config/xf/config.json"}}, Warnings: []string{}}
			if root != "" {
				v.Levels = append(v.Levels, SettingsLevel{Level: "project", Path: ".xf/config.json"})
			}
			userFile, projectFile := f.fileKeys("user"), f.fileKeys("project")
			for _, k := range []string{config.KeySourceAutosaveMs, config.KeyDbtTarget, config.KeyWebTheme, config.KeyAutoHidePane2, config.KeyPane2Tab, config.KeyWrapLines, config.KeyVimMode} {
				sk := SettingKey{Key: k, Live: k != config.KeyDbtTarget, UserOnly: config.UserOnly(k), From: "default"}
				if val, ok := f.user[k]; ok {
					sk.User, sk.Value, sk.From = &val, val, "~/.config/xf/config.json"
				}
				if val, ok := userFile[k]; ok {
					sk.User, sk.Value, sk.From = &val, val, "~/.config/xf/config.json"
				}
				if val, ok := f.project[k]; ok && root != "" {
					sk.Project, sk.Value, sk.From = &val, val, ".xf/config.json"
				}
				if val, ok := projectFile[k]; ok && root != "" {
					sk.Project, sk.Value, sk.From = &val, val, ".xf/config.json"
				}
				v.Keys = append(v.Keys, sk)
			}
			return v
		},
		Set: func(root, level, key, value string, unset bool) (string, error) {
			if !unset {
				if err := config.Check(key, value); err != nil {
					return "", err
				}
			}
			f.mu.Lock()
			defer f.mu.Unlock()
			m := f.user
			if level == "project" {
				m = f.project
			}
			if unset {
				delete(m, key)
			} else {
				m[key] = value
			}
			return level + ".json", nil
		},
		File: func(root, level string) (string, error) {
			return filepath.Join(f.dir, level+".json"), nil
		},
	}
}

func newSettingsTest(t *testing.T, f *fakeSettings, opens *int) (*Server, *httptest.Server, *http.Client) {
	t.Helper()
	open := func() *Project {
		return &Project{Deps: frontend.Deps{Root: "/work/proj", Engine: newFakeEngine(testGraph())}}
	}
	s, err := New(Config{Token: "secret", Project: open(), Settings: f.hook(),
		Projects: func() []session.Project { return []session.Project{{Root: "/work/proj"}} },
		Open: func(session.Project) (*Project, error) {
			*opens++
			return open(), nil
		}})
	if err != nil {
		t.Fatal(err)
	}
	ts := httptest.NewServer(s.Handler())
	t.Cleanup(func() { ts.Close(); s.Close() })
	c := ts.Client()
	jar := &tokenJar{}
	jar.set(ts.URL)
	c.Jar = jar
	return s, ts, c
}

func TestSettingsApplyLiveAndSayWhatWaits(t *testing.T) {
	f := &fakeSettings{user: map[string]string{}, project: map[string]string{}}
	opens := 0
	s, ts, c := newSettingsTest(t, f, &opens)
	events := openEvents(t, c, ts.URL, "proj")

	var a settingsAnswer
	getJSON(t, c, ts.URL+"/api/settings", &a)
	if a.Root != "/work/proj" || len(a.Levels) != 2 || len(a.Pending) != 0 {
		t.Fatalf("settings = %+v", a)
	}

	// A live key applies at once: the page's boot and every page hear it.
	postJSON(t, c, ts.URL+"/api/settings", map[string]any{"level": "user", "key": config.KeySourceAutosaveMs, "value": "1200"}, &a)
	if a.Saved != "user.json" || len(a.Pending) != 0 {
		t.Errorf("after a live change: %+v", a)
	}
	events.until(t, "settings", `"sourceAutosaveMs":1200`)
	var boot projectBoot
	getJSON(t, c, ts.URL+"/api/boot?project="+s.startKey, &struct{ Project *projectBoot }{&boot})
	if boot.SourceAutosaveMs != 1200 {
		t.Errorf("boot sourceAutosaveMs = %d, want 1200", boot.SourceAutosaveMs)
	}

	// A project value wins over the user's.
	postJSON(t, c, ts.URL+"/api/settings", map[string]any{"level": "project", "key": config.KeySourceAutosaveMs, "value": "100"}, nil)
	events.until(t, "settings", `"sourceAutosaveMs":500`) // the floor

	// A key that applies on reopen waits, and the page is told.
	postJSON(t, c, ts.URL+"/api/settings", map[string]any{"level": "project", "key": config.KeyDbtTarget, "value": "prod"}, &a)
	if len(a.Pending) != 1 || a.Pending[0] != config.KeyDbtTarget {
		t.Errorf("pending = %v, want dbtTarget", a.Pending)
	}
	// Taking it out again leaves nothing waiting.
	postJSON(t, c, ts.URL+"/api/settings", map[string]any{"level": "project", "key": config.KeyDbtTarget, "unset": true}, &a)
	if len(a.Pending) != 0 {
		t.Errorf("pending after unset = %v", a.Pending)
	}

	// Reopening opens the project again, and then nothing waits.
	postJSON(t, c, ts.URL+"/api/settings", map[string]any{"level": "project", "key": config.KeyDbtTarget, "value": "prod"}, nil)
	postJSON(t, c, ts.URL+"/api/reopen", nil, nil)
	events.until(t, "reload", "")
	if opens != 1 {
		t.Errorf("opens = %d, want 1", opens)
	}
	getJSON(t, c, ts.URL+"/api/settings", &a)
	if len(a.Pending) != 0 {
		t.Errorf("pending after reopen = %v", a.Pending)
	}
}

// autoHidePane2 is on unless a file says false, and a change applies at
// once.
func TestSettingsAutoHidePane2(t *testing.T) {
	f := &fakeSettings{user: map[string]string{}, project: map[string]string{}}
	opens := 0
	s, ts, c := newSettingsTest(t, f, &opens)
	events := openEvents(t, c, ts.URL, "proj")

	postJSON(t, c, ts.URL+"/api/settings", map[string]any{"level": "user", "key": config.KeyAutoHidePane2, "value": "false"}, nil)
	events.until(t, "settings", `"autoHidePane2":false`)
	var boot projectBoot
	getJSON(t, c, ts.URL+"/api/boot?project="+s.startKey, &struct{ Project *projectBoot }{&boot})
	if boot.AutoHidePane2 {
		t.Error("boot autoHidePane2 = true after setting false")
	}

	postJSON(t, c, ts.URL+"/api/settings", map[string]any{"level": "user", "key": config.KeyAutoHidePane2, "unset": true}, nil)
	events.until(t, "settings", `"autoHidePane2":true`)
	getJSON(t, c, ts.URL+"/api/boot?project="+s.startKey, &struct{ Project *projectBoot }{&boot})
	if !boot.AutoHidePane2 {
		t.Error("boot autoHidePane2 = false with nothing set, want the default, true")
	}
}

// pane2Tab is sql unless a file says columns, and every page is told of a
// change at once.
func TestSettingsPane2Tab(t *testing.T) {
	f := &fakeSettings{user: map[string]string{}, project: map[string]string{}}
	opens := 0
	s, ts, c := newSettingsTest(t, f, &opens)
	events := openEvents(t, c, ts.URL, "proj")
	var boot projectBoot
	getJSON(t, c, ts.URL+"/api/boot?project="+s.startKey, &struct{ Project *projectBoot }{&boot})
	if boot.Pane2Tab != "sql" {
		t.Errorf("boot pane2Tab = %q with nothing set, want the default, sql", boot.Pane2Tab)
	}

	postJSON(t, c, ts.URL+"/api/settings", map[string]any{"level": "user", "key": config.KeyPane2Tab, "value": "columns"}, nil)
	events.until(t, "settings", `"pane2Tab":"columns"`)
	getJSON(t, c, ts.URL+"/api/boot?project="+s.startKey, &struct{ Project *projectBoot }{&boot})
	if boot.Pane2Tab != "columns" {
		t.Errorf("boot pane2Tab = %q after setting columns", boot.Pane2Tab)
	}
}

// wrapLines is off unless a file says true, and every page is told of a
// change at once.
func TestSettingsWrapLines(t *testing.T) {
	f := &fakeSettings{user: map[string]string{}, project: map[string]string{}}
	opens := 0
	s, ts, c := newSettingsTest(t, f, &opens)
	events := openEvents(t, c, ts.URL, "proj")
	var boot projectBoot
	getJSON(t, c, ts.URL+"/api/boot?project="+s.startKey, &struct{ Project *projectBoot }{&boot})
	if boot.WrapLines {
		t.Error("boot wrapLines = true with nothing set, want the default, false")
	}

	postJSON(t, c, ts.URL+"/api/settings", map[string]any{"level": "user", "key": config.KeyWrapLines, "value": "true"}, nil)
	events.until(t, "settings", `"wrapLines":true`)
	getJSON(t, c, ts.URL+"/api/boot?project="+s.startKey, &struct{ Project *projectBoot }{&boot})
	if !boot.WrapLines {
		t.Error("boot wrapLines = false after setting true")
	}
}

// vimMode is off unless a file says true, and every page is told of a
// change at once, which switches its editors.
func TestSettingsVimMode(t *testing.T) {
	f := &fakeSettings{user: map[string]string{}, project: map[string]string{}}
	opens := 0
	s, ts, c := newSettingsTest(t, f, &opens)
	events := openEvents(t, c, ts.URL, "proj")
	var boot projectBoot
	getJSON(t, c, ts.URL+"/api/boot?project="+s.startKey, &struct{ Project *projectBoot }{&boot})
	if boot.VimMode {
		t.Error("boot vimMode = true with nothing set, want the default, false")
	}

	postJSON(t, c, ts.URL+"/api/settings", map[string]any{"level": "user", "key": config.KeyVimMode, "value": "true"}, nil)
	events.until(t, "settings", `"vimMode":true`)
	getJSON(t, c, ts.URL+"/api/boot?project="+s.startKey, &struct{ Project *projectBoot }{&boot})
	if !boot.VimMode {
		t.Error("boot vimMode = false after setting true")
	}
	if opens != 0 {
		t.Errorf("vimMode reopened the project %d times, want it live", opens)
	}
}

func TestSettingsRefuse(t *testing.T) {
	f := &fakeSettings{user: map[string]string{}, project: map[string]string{}}
	opens := 0
	_, ts, c := newSettingsTest(t, f, &opens)
	for _, tt := range []struct {
		body map[string]any
		want string
	}{
		{map[string]any{"level": "project", "key": config.KeyWebAutoExitSeconds, "value": "60"}, "user config only"},
		{map[string]any{"level": "user", "key": config.KeySourceAutosaveMs, "value": "soon"}, "whole number"},
		{map[string]any{"level": "user", "key": "nope", "value": "x"}, "unknown key"},
	} {
		var e errorView
		if st := postStatus(t, c, ts.URL+"/api/settings", tt.body, &e); st != http.StatusUnprocessableEntity || !strings.Contains(e.Error, tt.want) {
			t.Errorf("%v: status %d %q, want 422 saying %q", tt.body, st, e.Error, tt.want)
		}
	}
	if st := postStatus(t, c, ts.URL+"/api/settings", map[string]any{"level": "team", "key": config.KeyEditor}, nil); st != http.StatusBadRequest {
		t.Errorf("bad level: status %d", st)
	}
	if len(f.user)+len(f.project) != 0 {
		t.Errorf("refused changes were written: %v %v", f.user, f.project)
	}
}

func TestSettingsOutsideAProject(t *testing.T) {
	f := &fakeSettings{user: map[string]string{}, project: map[string]string{}}
	s, err := New(Config{Token: "secret", Settings: f.hook(), Projects: func() []session.Project { return nil }})
	if err != nil {
		t.Fatal(err)
	}
	ts := httptest.NewServer(s.Handler())
	t.Cleanup(func() { ts.Close(); s.Close() })
	c := ts.Client()
	jar := &tokenJar{}
	jar.set(ts.URL)
	c.Jar = jar
	var a settingsAnswer
	getJSON(t, c, ts.URL+"/api/settings", &a)
	if a.Root != "" || len(a.Levels) != 1 || a.Levels[0].Level != "user" {
		t.Fatalf("outside a project: %+v", a)
	}
	postJSON(t, c, ts.URL+"/api/settings", map[string]any{"level": "user", "key": config.KeySourceAutosaveMs, "value": "1500"}, nil)
	var e errorView
	if st := postStatus(t, c, ts.URL+"/api/settings", map[string]any{"level": "project", "key": config.KeyEditor, "value": "code"}, &e); st == http.StatusOK {
		t.Error("set a project key with no project open")
	}
}

// The JSON view saves a file whole: only text that checks, only over the
// file it loaded, and answers as a single key's save does.
func TestSettingsFileSave(t *testing.T) {
	f := &fakeSettings{user: map[string]string{}, project: map[string]string{}, dir: t.TempDir()}
	opens := 0
	_, ts, c := newSettingsTest(t, f, &opens)
	events := openEvents(t, c, ts.URL, "proj")
	projectFile := filepath.Join(f.dir, "project.json")
	onDisk := func() string { b, _ := os.ReadFile(projectFile); return string(b) }

	// No file yet: empty, not found, with the hash a save names.
	var file settingsFileView
	getJSON(t, c, ts.URL+"/api/settings/file?level=project", &file)
	if file.Found || file.Text != "" || file.Hash != hashOf(nil) {
		t.Fatalf("missing file = %+v", file)
	}
	save := func(text, hash string, out any) int {
		return postStatus(t, c, ts.URL+"/api/settings/file", map[string]any{"level": "project", "text": text, "hash": hash}, out)
	}

	// Refused, with the line, and nothing written: broken JSON, a bad
	// value, a key of the user's file only.
	for _, tt := range []struct {
		name, text string
		line       int
		key, want  string
	}{
		{"broken", "{\n  \"dbtTarget\": \"prod\"\n  \"editor\": \"code\"\n}\n", 3, "", "invalid character"},
		{"bad value", "{\n  \"dbtTarget\": \"prod\",\n  \"sourceAutosaveMs\": \"soon\"\n}\n", 3, config.KeySourceAutosaveMs, "whole number"},
		{"user-only", "{\n  \"webAutoExitSeconds\": 60\n}\n", 2, config.KeyWebAutoExitSeconds, "user config only"},
	} {
		var e settingsFileRefusal
		if st := save(tt.text, file.Hash, &e); st != http.StatusUnprocessableEntity || e.Kind != "invalid" {
			t.Errorf("%s: status %d %+v, want 422 invalid", tt.name, st, e)
			continue
		}
		if len(e.Marks) == 0 || e.Marks[0].Line != tt.line || e.Marks[0].Key != tt.key || e.Marks[0].Severity != "error" || !strings.Contains(e.Marks[0].Message, tt.want) {
			t.Errorf("%s: marks %+v, want line %d key %q saying %q", tt.name, e.Marks, tt.line, tt.key, tt.want)
		}
		if !strings.Contains(e.Error, "line ") {
			t.Errorf("%s: error %q doesn't name the line", tt.name, e.Error)
		}
	}
	if _, err := os.Stat(projectFile); err == nil {
		t.Fatalf("a refused save wrote the file: %q", onDisk())
	}

	// A good save writes the text as typed, answers with the new view and
	// what waits for the reopen, and the live keys reach the pages; an
	// unknown key is written and marked.
	text := "{\n  \"dbtTarget\": \"prod\",\n  \"sourceAutosaveMs\": 1800,\n  \"dataformLocaton\": \"eu\"\n}\n"
	var a settingsFileAnswer
	if st := save(text, file.Hash, &a); st != http.StatusOK {
		t.Fatalf("good save: status %d", st)
	}
	if onDisk() != text {
		t.Errorf("file = %q, want %q", onDisk(), text)
	}
	if a.File.Hash != hashOf([]byte(text)) || !a.File.Found || a.Saved != projectFile {
		t.Errorf("answer file = %+v saved %q", a.File, a.Saved)
	}
	if len(a.Pending) != 1 || a.Pending[0] != config.KeyDbtTarget {
		t.Errorf("pending = %v, want dbtTarget", a.Pending)
	}
	if valueOf(a.SettingsView, config.KeySourceAutosaveMs) != "1800" {
		t.Errorf("view after save: %+v", a.Keys)
	}
	if len(a.Marks) != 1 || a.Marks[0].Line != 4 || a.Marks[0].Severity != "warning" || !strings.Contains(a.Marks[0].Message, "unknown key \"dataformLocaton\"") {
		t.Errorf("marks = %+v, want the unknown key on line 4", a.Marks)
	}
	events.until(t, "settings", `"sourceAutosaveMs":1800`)

	// A save over a file that changed since: 409, with the file now.
	if err := os.WriteFile(projectFile, []byte("{\"dbtTarget\": \"dev\"}\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	var e settingsFileRefusal
	if st := save("{}\n", a.File.Hash, &e); st != http.StatusConflict || e.Kind != "conflict" || e.Current == nil || e.Current.Text != "{\"dbtTarget\": \"dev\"}\n" || e.Current.Hash != hashOf([]byte(e.Current.Text)) {
		t.Errorf("stale save: status %d %+v", st, e)
	}
	if onDisk() != "{\"dbtTarget\": \"dev\"}\n" {
		t.Errorf("a stale save wrote: %q", onDisk())
	}

	// The level must be one of the two.
	if st := postStatus(t, c, ts.URL+"/api/settings/file", map[string]any{"level": "team", "text": "{}", "hash": ""}, nil); st != http.StatusBadRequest {
		t.Errorf("bad level: status %d", st)
	}
}

// The user's file takes the user-only keys; outside a project there is no
// project file to read or write.
func TestSettingsFileUserLevel(t *testing.T) {
	f := &fakeSettings{user: map[string]string{}, project: map[string]string{}, dir: t.TempDir()}
	s, err := New(Config{Token: "secret", Settings: f.hook(), Projects: func() []session.Project { return nil }})
	if err != nil {
		t.Fatal(err)
	}
	ts := httptest.NewServer(s.Handler())
	t.Cleanup(func() { ts.Close(); s.Close() })
	c := ts.Client()
	jar := &tokenJar{}
	jar.set(ts.URL)
	c.Jar = jar
	var a settingsFileAnswer
	if st := postStatus(t, c, ts.URL+"/api/settings/file", map[string]any{"level": "user", "text": "{\"webTheme\": \"dracula\"}\n", "hash": hashOf(nil)}, &a); st != http.StatusOK {
		t.Fatalf("user save: status %d", st)
	}
	if valueOf(a.SettingsView, config.KeyWebTheme) != "dracula" {
		t.Errorf("view after save: %+v", a.Keys)
	}
	var e errorView
	if st := postStatus(t, c, ts.URL+"/api/settings/file", map[string]any{"level": "project", "text": "{}", "hash": hashOf(nil)}, &e); st != http.StatusUnprocessableEntity || !strings.Contains(e.Error, "no project") {
		t.Errorf("project save outside a project: status %d %q", st, e.Error)
	}
	if st := getStatus(t, c, ts.URL+"/api/settings/file?level=project"); st != http.StatusUnprocessableEntity {
		t.Errorf("project file outside a project: status %d", st)
	}
}

// The JSON view's save of a worktree's config, a link to its own copy
// (worktree.Link), writes the copy and leaves the link.
func TestWriteSettingsFileThroughLink(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("the config is a copy there, not a link")
	}
	dir := t.TempDir()
	own, at := filepath.Join(dir, "own.json"), filepath.Join(dir, "config.json")
	if err := os.WriteFile(own, []byte("{}\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(own, at); err != nil {
		t.Fatal(err)
	}
	if err := writeSettingsFile(at, []byte(`{"tablePrefix":"wt"}`)); err != nil {
		t.Fatal(err)
	}
	if l, err := os.Readlink(at); err != nil || l != own {
		t.Errorf("the link is %q, %v", l, err)
	}
	if b, _ := os.ReadFile(own); string(b) != `{"tablePrefix":"wt"}` {
		t.Errorf("the copy is %q", b)
	}
}
