package frontend

import (
	"cmp"
	"context"
	"strings"
	"sync"

	"github.com/charmbracelet/x/ansi"

	"github.com/ashish10alex/xf/internal/gcpauth"
)

// InfoRead is what the , overlay has read since it opened: the tool's
// version, the git state and the Google accounts, each Done once its
// answer arrived. LastEditor is the file an editor last said is active,
// and Following whether the selection follows it.
type InfoRead struct {
	Version     string
	VersionErr  error
	VersionDone bool
	// FormatVersion is the version of ProjectInfo.Format's program.
	FormatVersion string
	FormatErr     error
	FormatDone    bool
	Git           InfoGit
	GitErr        error
	GitDone       bool
	ADC           gcpauth.ADC
	ADCDone       bool
	Gcloud        gcpauth.CLI
	GcloudDone    bool
	LastEditor    string
	Following     bool
}

// InfoEntry is a section's title or a row of the , overlay, before it is
// laid out: a label padded to LabelW cells, a value padded to ValueW and a
// dim note. Section and Row name it, which the values that arrive late
// don't change; row 0 is the section's title.
type InfoEntry struct {
	Section            string
	Row                int
	Title              string
	Label, Value, Note string
	LabelW, ValueW     int
	Warn               bool // the value is a warning
}

// InfoEntries are the , overlay's sections and their rows for deps, with
// what has been read so far. A row with neither a value nor a note is left
// out.
func InfoEntries(deps Deps, o InfoRead) []InfoEntry {
	info := deps.Info
	if info == nil {
		return nil
	}
	tool := cmp.Or(deps.Backend, "dataform")
	var entries []InfoEntry
	var at InfoEntry
	section := func(title string) {
		at = InfoEntry{Section: title}
		entries = append(entries, InfoEntry{Section: title, Title: title})
	}
	add := func(e InfoEntry) {
		if e.Value == "" && e.Note == "" {
			return
		}
		at.Row++
		e.Section, e.Row = at.Section, at.Row
		entries = append(entries, e)
	}
	// warnRows adds a section's rows, their labels padded to the widest,
	// those warn says are problems in amber; rows adds them all plain.
	warnRows := func(warn func([3]string) bool, rs ...[3]string) {
		labelW := 0
		for _, r := range rs {
			labelW = max(labelW, ansi.StringWidth(r[0]))
		}
		for _, r := range rs {
			add(InfoEntry{Label: r[0], LabelW: labelW, Value: r[1], Note: r[2], Warn: warn != nil && warn(r)})
		}
	}
	rows := func(rs ...[3]string) { warnRows(nil, rs...) }

	section("project")
	project := [][3]string{{"root", deps.Root, ""}, {"backend", tool, info.BackendFrom}}
	if info.Target != "" {
		project = append(project, [3]string{"target", info.Target, info.TargetFrom})
	}
	rows(project...)

	section("binaries")
	version := "…"
	switch {
	case info.ToolVersion == nil:
		version = ""
	case o.VersionDone && o.VersionErr != nil:
		version = "(unknown) " + FirstLine(o.VersionErr.Error())
	case o.VersionDone:
		version = o.Version
	}
	toolWarn := func(r [3]string) bool {
		return r[0] == tool && (r[1] == "" || strings.HasPrefix(r[1], "(not found)") || (o.VersionDone && o.VersionErr != nil))
	}
	binaries := [][3]string{
		{"xf", info.Exe, ""},
		{"", info.Build, ""},
		{tool, info.ToolBin, info.ToolFrom},
		{"", version, ""},
	}
	if f := info.Format; f != nil {
		fv := ""
		switch {
		case o.FormatDone && o.FormatErr != nil:
			fv = "(unknown) " + FirstLine(o.FormatErr.Error())
		case o.FormatDone:
			fv = o.FormatVersion
		}
		binaries = append(binaries, [3]string{"sqlfluff", f.Bin, f.From}, [3]string{"", fv, ""})
		if f.Config != "" {
			binaries = append(binaries, [3]string{"sqlfluff config", f.Config, f.ConfigFrom})
		}
	}
	warnRows(toolWarn, binaries...)

	section("config files, lowest precedence first")
	pathW := 0
	for _, f := range info.Files {
		pathW = max(pathW, ansi.StringWidth(f.Path))
	}
	for _, f := range info.Files {
		state := "not found"
		if f.Found {
			state = "read"
		}
		add(InfoEntry{Value: f.Path, ValueW: pathW, Note: state})
	}

	section("settings")
	keyW, valueW := 0, 0
	for _, r := range info.Settings {
		keyW, valueW = max(keyW, ansi.StringWidth(r.Key)), max(valueW, ansi.StringWidth(r.Value))
	}
	for _, r := range info.Settings {
		add(InfoEntry{Label: r.Key, LabelW: keyW, Value: r.Value, ValueW: valueW, Note: r.From})
	}

	if info.ADC != nil || info.Gcloud != nil {
		section("google cloud")
		var cloud [][3]string
		switch {
		case info.ADC == nil:
		case o.ADCDone:
			cloud = o.ADC.Rows(deps.Backend == "dbt")
		default:
			cloud = [][3]string{{"xf uses", "…", ""}}
		}
		switch {
		case info.Gcloud == nil:
		case o.GcloudDone:
			cloud = append(cloud, o.Gcloud.Rows(o.ADC)...)
		default:
			cloud = append(cloud, [3]string{"gcloud", "…", ""})
		}
		warnRows(gcpauth.Warn, cloud...)
	}

	if info.Git != nil {
		section("git")
		branch, origin := "…", "…"
		switch {
		case o.GitDone && o.GitErr != nil:
			branch, origin = "(not a git repository)", ""
		case o.GitDone:
			branch, origin = cmp.Or(o.Git.Branch, "(detached HEAD)"), cmp.Or(o.Git.Origin, "(no origin remote)")
		}
		git := [][3]string{{"branch", branch, ""}, {"origin", origin, ""}}
		if deps.DefaultBranch != "" {
			git = append(git, [3]string{"compares with", deps.DefaultBranch, "R looks for what changed since it"})
		}
		rows(git...)
	}

	section("editor")
	rows(infoEditor(deps, o)...)

	section("engine")
	rows([3]string{"socket", info.Socket, ""}, [3]string{"log", info.Log, ""})

	if len(info.Warnings) > 0 {
		section("warnings")
		for _, warning := range info.Warnings {
			for _, l := range strings.Split(strings.TrimRight(warning, "\n"), "\n") {
				add(InfoEntry{Value: l, Warn: true})
			}
		}
	}
	return entries
}

// infoEditor says which editor xf works with now: the one that last said
// which file is active, or the neovim it runs beside, or the editor
// setting's command.
func infoEditor(deps Deps, o InfoRead) [][3]string {
	following := "following off (F)"
	if o.Following {
		following = "following on"
	}
	switch {
	case o.LastEditor != "":
		return [][3]string{{"connected", "yes", following}, {"active file", o.LastEditor, ""}}
	case deps.FullScreen != nil:
		return [][3]string{{"connected", "neovim, beside xf", following}}
	}
	out := [][3]string{{"connected", "no", "no editor has said which file is active"}}
	if deps.Open != nil && deps.Info.EditorCommand != "" {
		out = append(out, [3]string{"o opens with", deps.Info.EditorCommand, ""})
	}
	return out
}

// InfoRow is a row of the , overlay: a section's title, or a label, a
// value and a dim note.
type InfoRow struct {
	Title string `json:"title,omitempty"`
	Label string `json:"label,omitempty"`
	Value string `json:"value,omitempty"`
	Note  string `json:"note,omitempty"`
	Warn  bool   `json:"warn,omitempty"`
}

// ProjectInfoRows are the , overlay's rows for deps, with the tool's
// version, the git state and the Google accounts asked for now, each
// within InfoTimeout. lastEditor is the file an editor last said is
// active, and following whether the selection follows it.
func ProjectInfoRows(ctx context.Context, deps Deps, lastEditor string, following bool) []InfoRow {
	var out []InfoRow
	ProjectInfoStream(ctx, deps, lastEditor, following, func(rows []InfoRow) { out = rows })
	return out
}

// ProjectInfoStream gives emit the rows ProjectInfoRows answers with as
// they arrive: at once, with "…" for each lookup still going, and again
// as each lands, so a slow one (gcloud, a token refresh) holds up only
// its own rows. It returns after the last; emit is never called twice at
// once.
func ProjectInfoStream(ctx context.Context, deps Deps, lastEditor string, following bool, emit func([]InfoRow)) {
	if deps.Info == nil {
		emit(nil)
		return
	}
	info := deps.Info
	o := InfoRead{LastEditor: lastEditor, Following: following}
	var mu sync.Mutex
	send := func() { // with mu held
		var out []InfoRow
		for _, e := range InfoEntries(deps, o) {
			out = append(out, InfoRow{Title: e.Title, Label: e.Label, Value: e.Value, Note: e.Note, Warn: e.Warn})
		}
		emit(out)
	}
	mu.Lock()
	send()
	mu.Unlock()
	var wg sync.WaitGroup
	// ask runs a lookup, then under mu sets what it read and sends.
	ask := func(read func(ctx context.Context) func(o *InfoRead)) {
		wg.Go(func() {
			ctx, cancel := context.WithTimeout(ctx, InfoTimeout)
			defer cancel()
			set := read(ctx)
			mu.Lock()
			defer mu.Unlock()
			set(&o)
			send()
		})
	}
	if f := info.ToolVersion; f != nil {
		ask(func(ctx context.Context) func(*InfoRead) {
			v, err := f(ctx)
			return func(o *InfoRead) { o.Version, o.VersionErr, o.VersionDone = v, err, true }
		})
	}
	if f := info.Format; f != nil && f.Version != nil {
		ask(func(ctx context.Context) func(*InfoRead) {
			v, err := f.Version(ctx)
			return func(o *InfoRead) { o.FormatVersion, o.FormatErr, o.FormatDone = v, err, true }
		})
	}
	if f := info.Git; f != nil {
		ask(func(ctx context.Context) func(*InfoRead) {
			g, err := f(ctx)
			return func(o *InfoRead) { o.Git, o.GitErr, o.GitDone = g, err, true }
		})
	}
	if f := info.ADC; f != nil {
		ask(func(ctx context.Context) func(*InfoRead) {
			a := f(ctx)
			return func(o *InfoRead) { o.ADC, o.ADCDone = a, true }
		})
	}
	if f := info.Gcloud; f != nil {
		ask(func(ctx context.Context) func(*InfoRead) {
			c := f(ctx)
			return func(o *InfoRead) { o.Gcloud, o.GcloudDone = c, true }
		})
	}
	wg.Wait()
}

// SetInfoSetting shows key as set to value in from in the , overlay's
// settings.
func SetInfoSetting(info *ProjectInfo, key, value, from string) {
	if info == nil {
		return
	}
	for i, s := range info.Settings {
		if s.Key == key {
			info.Settings[i].Value, info.Settings[i].From = value, from
		}
	}
}
