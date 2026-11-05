package web

import (
	"bytes"
	"cmp"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"strconv"

	"github.com/ashish10alex/xf/internal/config"
)

// The settings page edits xf's two config files, the user's and the open
// project's .xf/config.json, the files the TUI and xf lsp read too. A
// change some part of xf web reads as it goes (the source pane's autosave,
// the Scratch view's sizes, the editor o uses, the themes, the base R
// compares with) applies at once; the rest apply when the project reopens, which the page offers.

// Settings is what the settings page needs of xf's config files; cmd/xf
// knows each key's default and where a value comes from.
type Settings struct {
	// View is the settings of the project at root, whose tool is tool
	// ("dataform" or "dbt"); root "" is outside any project, with only the
	// user's file.
	View func(root, tool string) SettingsView
	// Set writes key's value at level ("user" or "project") for root, or
	// with unset takes the key out of that file. It fails for a value the
	// file wouldn't take, and answers with the file it wrote.
	Set func(root, level, key, value string, unset bool) (file string, err error)
	// File is the config file of level for root, an absolute path, which
	// the page's JSON view reads and writes whole.
	File func(root, level string) (string, error)
}

// SettingsView is the page's settings: the files, and each key.
type SettingsView struct {
	Levels []SettingsLevel `json:"levels"`
	// Warnings are the files' problems the loader warns of, such as
	// unknown keys, each with its file and line.
	Warnings []string     `json:"warnings"`
	Keys     []SettingKey `json:"keys"`
}

// SettingsLevel is one of the files: "user" or "project".
type SettingsLevel struct {
	Level string `json:"level"`
	Path  string `json:"path"`
	Found bool   `json:"found"`
	// Error is why the page can't edit this level, such as a file that
	// isn't JSON; "" when it can.
	Error string `json:"error,omitempty"`
}

// SettingKey is a key as the page shows it.
type SettingKey struct {
	Key string `json:"key"`
	// Title is the key's name as a person reads it, e.g. "Autosave delay".
	Title   string `json:"title"`
	Section string `json:"section"`
	Doc     string `json:"doc"`
	// Default is what the key is when nothing sets it, e.g. "main" or
	// "code ($VISUAL/$EDITOR first)"; "none" for nothing.
	Default string `json:"default"`
	// Hint is what a value looks like, e.g. "milliseconds, e.g. 1500".
	Hint string `json:"hint"`
	// Kind picks the control: "choice", "bool", "number" or "text".
	Kind    string   `json:"kind"`
	Choices []string `json:"choices,omitempty"`
	// UserOnly keys are set in the user's file only (the themes).
	UserOnly bool `json:"userOnly,omitempty"`
	// Live keys apply at once; the others when the project reopens.
	Live bool `json:"live"`
	// User and Project are what each file says, nil when it says nothing;
	// Flag is the value a command-line flag gives this run, nil for none.
	User    *string `json:"user"`
	Project *string `json:"project"`
	Flag    *string `json:"flag"`
	// Value is the value in use and From where it comes from.
	Value string `json:"value"`
	From  string `json:"from"`
}

// settingsAnswer is what the page is told of the settings: the view, and
// for an open project the changes waiting for it to reopen.
type settingsAnswer struct {
	SettingsView
	// Root is the open project, "" for none (only the user's file).
	Root string `json:"root"`
	// Pending are the keys whose value in use changed since the project
	// opened but only applies when it reopens.
	Pending []string `json:"pending"`
	// Saved is the file a change was written to.
	Saved string `json:"saved,omitempty"`
}

// settingsEvent is what every page is told after a change: what it reads
// of the settings itself.
type settingsEvent struct {
	SourceAutosaveMs int               `json:"sourceAutosaveMs"`
	VimKeys          map[string]string `json:"vimKeys"`
	DiffStyle        string            `json:"diffStyle"`
	AutoHidePane2    bool              `json:"autoHidePane2"`
	Pane2Tab         string            `json:"pane2Tab"`
	WrapLines        bool              `json:"wrapLines"`
	VimMode          bool              `json:"vimMode"`
	// OpenIn is webOpenProjectIn: same-tab or new-tab.
	OpenIn string `json:"openIn"`
}

// diffStyleOf is the diffStyle the page is told: inline unless set to
// side-by-side.
func diffStyleOf(v string) string {
	if v == config.DiffSideBySide {
		return v
	}
	return config.DiffInline
}

// pane2TabOf is the pane2Tab the page is told: sql unless set to
// columns.
func pane2TabOf(v string) string {
	if v == config.Pane2Columns {
		return v
	}
	return config.Pane2SQL
}

// reopenValues are the values in use of the keys that apply on reopen.
func reopenValues(v SettingsView) map[string]string {
	out := map[string]string{}
	for _, k := range v.Keys {
		if !k.Live {
			out[k.Key] = k.Value
		}
	}
	return out
}

// settingsTarget is the project the page is on, with its root and tool,
// "" outside one.
func (s *Server) settingsTarget(r *http.Request) (*projectState, string, string) {
	op := s.projectOf(r)
	if op == nil || op.p.standalone {
		return nil, "", ""
	}
	return op.p, op.p.deps.Root, op.p.tool
}

func (s *Server) settingsAnswer(p *projectState, root, tool string) settingsAnswer {
	a := settingsAnswer{SettingsView: s.cfg.Settings.View(root, tool), Root: root, Pending: []string{}}
	if p != nil {
		for _, k := range a.Keys {
			if was, ok := p.openedWith[k.Key]; ok && was != k.Value {
				a.Pending = append(a.Pending, k.Key)
			}
		}
	}
	return a
}

// settings answers with the settings of the open project, or outside one
// with the user's.
func (s *Server) settings(w http.ResponseWriter, r *http.Request) {
	if s.cfg.Settings == nil {
		http.Error(w, "settings can't be changed here", http.StatusNotFound)
		return
	}
	p, root, tool := s.settingsTarget(r)
	writeJSON(w, s.settingsAnswer(p, root, tool))
}

// setSetting writes one key at one level, or takes it out, then applies
// what applies at once and tells every page.
func (s *Server) setSetting(w http.ResponseWriter, r *http.Request) {
	if s.cfg.Settings == nil {
		http.Error(w, "settings can't be changed here", http.StatusNotFound)
		return
	}
	var req struct {
		Level string `json:"level"`
		Key   string `json:"key"`
		Value string `json:"value"`
		Unset bool   `json:"unset"`
	}
	if !readJSON(w, r, &req) {
		return
	}
	p, root, tool := s.settingsTarget(r)
	switch {
	case req.Level != "user" && req.Level != "project":
		http.Error(w, "bad request: level is user or project", http.StatusBadRequest)
		return
	case req.Level == "project" && root == "":
		writeError(w, errors.New("no project is open"))
		return
	case req.Level == "project" && config.UserOnly(req.Key):
		writeError(w, errors.New(req.Key+" is set in your user config only"))
		return
	}
	file, err := s.cfg.Settings.Set(root, req.Level, req.Key, req.Value, req.Unset)
	if err != nil {
		writeError(w, err)
		return
	}
	a := s.settingsChanged(p, root, tool, req.Level, req.Key)
	a.Saved = file
	writeJSON(w, a)
}

// settingsChanged answers with the settings after keys changed at level:
// it applies what applies at once and tells every page.
func (s *Server) settingsChanged(p *projectState, root, tool, level string, keys ...string) settingsAnswer {
	a := s.settingsAnswer(p, root, tool)
	// A project's key reaches that project's pages; a user's, every
	// project's, each with its own values in use.
	s.mu.Lock()
	reach := []*projectState{}
	for _, op := range s.open {
		if level == "user" || op.p == p {
			reach = append(reach, op.p)
		}
	}
	s.mu.Unlock()
	for _, q := range reach {
		v := a.SettingsView
		if q != p {
			v = s.cfg.Settings.View(q.deps.Root, q.tool)
		}
		q.applySettings(v, keys...)
		q.hub.send("settings", s.settingsEventOf(v))
	}
	if level == "user" {
		s.themeChanged(nil, keys)
	} else {
		s.themeChanged(p, keys)
	}
	return a
}

// ---- the JSON view: a config file whole ----

// settingsFileView is a config file's text as the JSON view edits it,
// with the hash a save names; a file not made yet is "" and not found.
type settingsFileView struct {
	Level string `json:"level"`
	Text  string `json:"text"`
	Hash  string `json:"hash"`
	Found bool   `json:"found"`
}

// settingsMark is a problem on a line of the text: an "error" stopped the
// save, a "warning" (an unknown key) didn't.
type settingsMark struct {
	config.Problem
	Severity string `json:"severity"`
}

// settingsFileAnswer is a saved file's answer: the settings, as a single
// key's save answers, with the file as written and its warnings.
type settingsFileAnswer struct {
	settingsAnswer
	File  settingsFileView `json:"file"`
	Marks []settingsMark   `json:"marks"`
}

// settingsFileRefusal is a refused save's answer: why, with the marks of
// a text that doesn't check ("invalid") or the file on disk now
// ("conflict").
type settingsFileRefusal struct {
	Error   string            `json:"error"`
	Kind    string            `json:"kind"`
	Marks   []settingsMark    `json:"marks,omitempty"`
	Current *settingsFileView `json:"current,omitempty"`
}

func marksOf(severity string, problems []config.Problem) []settingsMark {
	out := []settingsMark{}
	for _, p := range problems {
		out = append(out, settingsMark{p, severity})
	}
	return out
}

// settingsFilePath is the file of the request's level, or an error the
// page shows.
func (s *Server) settingsFilePath(level, root string) (string, error) {
	switch {
	case level != "user" && level != "project":
		return "", errBadLevel
	case level == "project" && root == "":
		return "", errors.New("no project is open")
	}
	return s.cfg.Settings.File(root, level)
}

var errBadLevel = errors.New("bad request: level is user or project")

// readSettingsFile is the file at path as the view shows it.
func readSettingsFile(level, path string) (settingsFileView, error) {
	data, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return settingsFileView{Level: level, Hash: hashOf(nil)}, nil
	}
	if err != nil {
		return settingsFileView{}, err
	}
	return settingsFileView{Level: level, Text: string(data), Hash: hashOf(data), Found: true}, nil
}

// settingsFile answers with a level's file, for the JSON view.
func (s *Server) settingsFile(w http.ResponseWriter, r *http.Request) {
	if s.cfg.Settings == nil || s.cfg.Settings.File == nil {
		http.Error(w, "settings can't be changed here", http.StatusNotFound)
		return
	}
	_, root, _ := s.settingsTarget(r)
	level := r.URL.Query().Get("level")
	path, err := s.settingsFilePath(level, root)
	if errors.Is(err, errBadLevel) {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	if err != nil {
		writeError(w, err)
		return
	}
	f, err := readSettingsFile(level, path)
	if err != nil {
		writeError(w, err)
		return
	}
	writeJSON(w, f)
}

// saveSettingsFile writes a level's file whole, from the JSON view: only
// when the text is the file the view loaded (hash), else 409 with the
// file now; and only when it checks as each key's save does, else 422
// with the lines. Unknown keys are written, and answered as warnings.
func (s *Server) saveSettingsFile(w http.ResponseWriter, r *http.Request) {
	if s.cfg.Settings == nil || s.cfg.Settings.File == nil {
		http.Error(w, "settings can't be changed here", http.StatusNotFound)
		return
	}
	var req struct {
		Level string `json:"level"`
		Text  string `json:"text"`
		Hash  string `json:"hash"`
	}
	if !readJSON(w, r, &req) {
		return
	}
	p, root, tool := s.settingsTarget(r)
	path, err := s.settingsFilePath(req.Level, root)
	if errors.Is(err, errBadLevel) {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	if err != nil {
		writeError(w, err)
		return
	}
	refuse := func(status int, v settingsFileRefusal) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(status)
		_ = json.NewEncoder(w).Encode(v)
	}
	// One save at a time, so two can't both pass the hash.
	s.settingsWrite.Lock()
	defer s.settingsWrite.Unlock()
	was, err := readSettingsFile(req.Level, path)
	if err != nil {
		writeError(w, err)
		return
	}
	if was.Hash != req.Hash {
		refuse(http.StatusConflict, settingsFileRefusal{Error: "the file changed on disk", Kind: "conflict", Current: &was})
		return
	}
	errs, warns := config.CheckFile([]byte(req.Text), req.Level == "user")
	if len(errs) > 0 {
		msg := errs[0].Message
		if len(errs) > 1 {
			msg += fmt.Sprintf(" (and %d more)", len(errs)-1)
		}
		refuse(http.StatusUnprocessableEntity, settingsFileRefusal{Error: fmt.Sprintf("line %d: %s", errs[0].Line, msg), Kind: "invalid",
			Marks: append(marksOf("error", errs), marksOf("warning", warns)...)})
		return
	}
	if err := writeSettingsFile(path, []byte(req.Text)); err != nil {
		writeError(w, err)
		return
	}
	a := settingsFileAnswer{
		settingsAnswer: s.settingsChanged(p, root, tool, req.Level, changedKeys([]byte(was.Text), []byte(req.Text))...),
		File:           settingsFileView{Level: req.Level, Text: req.Text, Hash: hashOf([]byte(req.Text)), Found: true},
		Marks:          marksOf("warning", warns),
	}
	a.Saved = path
	writeJSON(w, a)
}

// writeSettingsFile writes data to path in one step, making the file and
// its folder when there are none. A path that is a link, as a worktree's
// config is (worktree.Link), writes the file it links to and stays a
// link.
func writeSettingsFile(path string, data []byte) error {
	if real, err := filepath.EvalSymlinks(path); err == nil {
		path = real
	}
	if _, err := os.Stat(path); err == nil {
		return replaceFile(path, data)
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	tmp, err := os.CreateTemp(filepath.Dir(path), ".xf-save-*.tmp")
	if err != nil {
		return err
	}
	name := tmp.Name()
	defer func() { _ = os.Remove(name) }() // a no-op once renamed
	if _, err := tmp.Write(data); err != nil {
		_ = tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	if err := os.Chmod(name, 0o644); err != nil {
		return err
	}
	return os.Rename(name, path)
}

// changedKeys are the known keys whose JSON differs between two texts of
// a file; every key when the old text isn't an object.
func changedKeys(was, now []byte) []string {
	var a, b map[string]json.RawMessage
	if json.Unmarshal(was, &a) != nil && len(bytes.TrimSpace(was)) > 0 {
		return config.Keys
	}
	_ = json.Unmarshal(now, &b) // it checked
	var out []string
	for _, k := range config.Keys {
		x, inA := a[k]
		y, inB := b[k]
		if inA != inB || !bytes.Equal(x, y) {
			out = append(out, k)
		}
	}
	return out
}

func (s *Server) settingsEventOf(v SettingsView) settingsEvent {
	ms, _ := strconv.Atoi(valueOf(v, config.KeySourceAutosaveMs))
	return settingsEvent{SourceAutosaveMs: autosaveMs(ms), VimKeys: vimKeysOf(v), DiffStyle: diffStyleOf(valueOf(v, config.KeyDiffStyle)), AutoHidePane2: valueOf(v, config.KeyAutoHidePane2) != "false", Pane2Tab: pane2TabOf(valueOf(v, config.KeyPane2Tab)), WrapLines: valueOf(v, config.KeyWrapLines) == "true", VimMode: valueOf(v, config.KeyVimMode) == "true", OpenIn: s.openIn()}
}

// vimKeysOf are the vim keys in use in v, by key.
func vimKeysOf(v SettingsView) map[string]string {
	out := map[string]string{}
	for _, k := range config.VimKeys {
		// A key with no default has no value in use to hand on.
		value := valueOf
		if config.VimKeyDefaults[k] == "" {
			value = setValueOf
		}
		if keys := value(v, k); keys != "" {
			out[k] = keys
		}
	}
	return out
}

// setValueOf is the value of key when a file or a flag sets it, else "":
// the value in use of a key that has a default worked out (a binary found
// on PATH) is a description, not something to hand on.
func setValueOf(v SettingsView, key string) string {
	for _, k := range v.Keys {
		if k.Key == key && (k.User != nil || k.Project != nil || k.Flag != nil) {
			return k.Value
		}
	}
	return ""
}

// valueOf is key's value in use in v.
func valueOf(v SettingsView, key string) string {
	for _, k := range v.Keys {
		if k.Key == key {
			return k.Value
		}
	}
	return ""
}

// applySettings takes the values in use of the keys xf web reads as it
// goes; changed are the keys that were just written.
func (p *projectState) applySettings(v SettingsView, changed ...string) {
	ms, _ := strconv.Atoi(valueOf(v, config.KeySourceAutosaveMs))
	warn, _ := strconv.ParseFloat(valueOf(v, config.KeyScratchWarnGB), 64)
	confirm, _ := strconv.ParseFloat(valueOf(v, config.KeyScratchConfirmGB), 64)
	p.mu.Lock()
	p.sourceAutosaveMs, p.scratchWarnGB, p.scratchConfirmGB = ms, warn, confirm
	p.vimKeys = vimKeysOf(v)
	p.diffStyle = valueOf(v, config.KeyDiffStyle)
	p.pythonServer = valueOf(v, config.KeyPythonLanguageServer)
	p.sqlfluffBin, p.sqlfluffConfig = setValueOf(v, config.KeySqlfluffBin), setValueOf(v, config.KeySqlfluffConfig)
	off := p.pythonServer == config.PythonOff
	p.autoHidePane2 = valueOf(v, config.KeyAutoHidePane2) != "false"
	p.pane2Tab = valueOf(v, config.KeyPane2Tab)
	p.wrapLines = valueOf(v, config.KeyWrapLines) == "true"
	p.vimMode = valueOf(v, config.KeyVimMode) == "true"
	p.mu.Unlock()
	if off {
		p.py.stop()
	}
	for _, k := range changed {
		switch k {
		case config.KeyEditor:
			if p.editor != nil && p.editor.Reload != nil {
				p.editor.Reload()
			}
		case config.KeyDefaultBranch:
			// As b does: R, I and C compare with it from now on.
			p.applyBase(cmp.Or(valueOf(v, config.KeyDefaultBranch), config.DefaultBranch))
		case config.KeyTheme:
			// As the TUI's View: theme does: every open TUI changes.
			if t := valueOf(v, config.KeyTheme); t == config.ThemeAuto || t == config.ThemeLight || t == config.ThemeDark {
				p.deps.Engine.SetTheme(t)
			}
		}
	}
}
