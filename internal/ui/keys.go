package ui

import "charm.land/bubbles/v2/key"

// keyMap holds every binding. Pane-specific bindings share keys across panes
// (j/k move in whichever pane is focused); the footer shows the bindings for
// the focused pane followed by the global ones.
type keyMap struct {
	// global
	Find, Compile, Run, Deps, Dependents, FullRefresh, API, SelectedOnly key.Binding
	RunChanged, RunTags, Impact, Cost                                    key.Binding
	ToggleFile, ToggleAll                                                key.Binding
	Incremental, Log, Errors, Help, Quit, Cancel, Follow                 key.Binding
	Info, Branch                                                         key.Binding
	Zoom, FullScreen, ToggleLineage, Escape, Open                        key.Binding
	NextInFile, PrevInFile                                               key.Binding
	NextPane, PrevPane                                                   key.Binding
	FocusSQL, FocusActions, FocusLineage, FocusRun                       key.Binding
	FocusLog                                                             key.Binding

	// movement
	Up, Down, Top, Bottom, HalfDown, HalfUp key.Binding

	// palette
	Accept key.Binding
	// FindKind is a word of the palette's query, not a key.
	FindKind key.Binding
	// Commands starts the palette's query, to list commands.
	Commands key.Binding

	// actions pane
	ShowSQL key.Binding

	// actions and lineage panes
	OpenTable, CopyTarget, CopyPath key.Binding

	// lineage pane
	Jump, Back key.Binding

	// log pane
	OpenURL key.Binding
	// RunTable swaps an API run's table and its log; OpenJob opens the
	// BigQuery job of the table's action.
	RunTable, OpenJob key.Binding
	// ClearHistory forgets the kept runs, from the log pane's history tab.
	ClearHistory key.Binding

	// Columns swaps the sql pane's SQL and the columns it returns; over the
	// columns, CopyColumn and CopyColumnType copy the one under the cursor.
	Columns, CopyColumn, CopyColumnType key.Binding

	// sql pane: a read-only vim buffer
	Left, Right, WordNext, WordPrev, LineStart, LineEnd key.Binding
	ScrollDown, ScrollUp                                key.Binding
	Visual, VisualLine, Yank, YankLine, OtherEnd        key.Binding
	// Search is Find's key in the zoomed sql pane; NextMatch and PrevMatch
	// repeat it.
	Search, SearchWord, SearchWordBack key.Binding

	// tags overlay: a query line, so its keys are no letters. TagOptions
	// leaves the query for the mode where the run's toggles work.
	ToggleTag, RunChecked, TagOptions key.Binding

	// branch overlay: a query line too. FetchBranches fetches origin.
	FetchBranches key.Binding
	// Base opens it to pick the branch R, I and C compare with.
	Base key.Binding

	// overlays
	Close, CopyReport key.Binding

	// project info overlay: the matches of its search
	NextMatch, PrevMatch key.Binding

	// prompts
	Confirm key.Binding
}

func newKeyMap() keyMap {
	b := func(keys []string, help, desc string) key.Binding {
		return key.NewBinding(key.WithKeys(keys...), key.WithHelp(help, desc))
	}
	return keyMap{
		Find:        b([]string{"/"}, "/", "find action"),
		Compile:     b([]string{"c"}, "c", "compile"),
		Run:         b([]string{"r"}, "r", "run"),
		RunChanged:  b([]string{"R"}, "R", "run changed actions"),
		RunTags:     b([]string{"T"}, "T", "run tags"),
		Impact:      b([]string{"I"}, "I", "column impact of changes"),
		Cost:        b([]string{"C"}, "C", "query cost of changes"),
		ToggleFile:  b([]string{"space"}, "space", "check/uncheck file"),
		ToggleAll:   b([]string{"x"}, "x", "check/uncheck all files"),
		Deps:        b([]string{"d"}, "d", "deps"),
		Dependents:  b([]string{"D"}, "D", "dependents"),
		FullRefresh: b([]string{"f"}, "f", "full refresh"),
		API:         b([]string{"a"}, "a", "run via Dataform API"),
		// Off, r runs every action in the selected action's file.
		SelectedOnly: b([]string{"s"}, "s", "run selected action only"),
		// v is visual mode in the SQL pane; it still toggles elsewhere.
		Incremental: b([]string{"i", "v"}, "i", "incremental/full"),
		Log:         b([]string{"L"}, "L", "show/hide log"),
		Follow:      b([]string{"F"}, "F", "follow editor on/off"),
		Zoom:        b([]string{"z"}, "z", "zoom pane"),
		FullScreen:  b([]string{"Z"}, "Z", "full screen in nvim"),
		// O, since o is "other end" in the SQL pane's visual mode.
		ToggleLineage: b([]string{"O"}, "O", "show/hide lineage"),
		Escape:        b([]string{"esc"}, "esc", "unzoom"),
		// o outside the SQL pane's visual mode, where it is "other end".
		Open:       b([]string{"o"}, "o", "open in editor"),
		NextInFile: b([]string{"]"}, "]", "next action in file"),
		PrevInFile: b([]string{"["}, "[", "prev action in file"),
		Errors:     b([]string{"e"}, "e", "errors"),
		Info:       b([]string{","}, ",", "project info"),
		// B outside the SQL pane, where it is "prev WORD".
		Branch:   b([]string{"B"}, "B", "switch branch"),
		Help:     b([]string{"?"}, "?", "help"),
		Quit:     b([]string{"q"}, "q", "quit"),
		Cancel:   b([]string{"ctrl+c"}, "ctrl+c", "cancel run / quit"),
		NextPane: b([]string{"tab"}, "tab", "next pane"),
		PrevPane: b([]string{"shift+tab"}, "shift+tab", "prev pane"),

		FocusSQL:     b([]string{"0"}, "0", "sql"),
		FocusActions: b([]string{"1"}, "1", "actions"),
		FocusLineage: b([]string{"2"}, "2", "lineage"),
		FocusRun:     b([]string{"3"}, "3", "run"),
		FocusLog:     b([]string{"4"}, "4", "log"),

		Up:       b([]string{"k", "up", "ctrl+p"}, "k/↑", "up"),
		Down:     b([]string{"j", "down", "ctrl+n"}, "j/↓", "down"),
		Top:      b([]string{"g", "home"}, "g", "top"),
		Bottom:   b([]string{"G", "end"}, "G", "bottom"),
		HalfDown: b([]string{"ctrl+d", "pgdown"}, "ctrl+d", "page down"),
		HalfUp:   b([]string{"ctrl+u", "pgup"}, "ctrl+u", "page up"),

		Accept:   b([]string{"enter"}, "enter", "select"),
		FindKind: b([]string{"$"}, "$kind", "find one kind"),
		Commands: b([]string{">"}, ">", "commands"),

		ShowSQL: b([]string{"enter"}, "enter", "show its sql"),

		OpenTable:  b([]string{"u"}, "u", "open in BigQuery"),
		CopyTarget: b([]string{"y"}, "y", "copy `table` name"),
		CopyPath:   b([]string{"Y"}, "Y", "copy file path (again: absolute)"),

		OpenURL:  b([]string{"u"}, "u", "open run in browser"),
		RunTable: b([]string{"t"}, "t", "API run's table/log"),
		OpenJob:  b([]string{"enter"}, "enter", "open BigQuery job"),
		// X, since x checks files in the R overlay.
		ClearHistory: b([]string{"X"}, "X", "clear history"),

		Jump: b([]string{"enter"}, "enter", "jump"),
		Back: b([]string{"backspace"}, "backspace", "back"),

		Columns:        b([]string{"S"}, "S", "sql/columns"),
		CopyColumn:     b([]string{"y"}, "y", "copy column name"),
		CopyColumnType: b([]string{"Y"}, "Y", "copy name and type"),

		Left:       b([]string{"h", "left"}, "h/l", "left/right"),
		Right:      b([]string{"l", "right"}, "l", "right"),
		WordNext:   b([]string{"w", "W"}, "w/W", "next word/WORD"),
		WordPrev:   b([]string{"b", "B"}, "b/B", "prev word/WORD"),
		LineStart:  b([]string{"0", "^"}, "0/^", "line start"),
		LineEnd:    b([]string{"$"}, "$", "line end"),
		ScrollDown: b([]string{"ctrl+e"}, "ctrl+e/y", "scroll line"),
		ScrollUp:   b([]string{"ctrl+y"}, "ctrl+y", "scroll line up"),
		Visual:     b([]string{"v"}, "v", "select"),
		VisualLine: b([]string{"V"}, "V", "select lines"),
		Yank:       b([]string{"y"}, "y", "copy selection"),
		YankLine:   b([]string{"Y"}, "yy/Y", "copy line"),
		OtherEnd:   b([]string{"o"}, "o", "other end"),

		Search:         b([]string{"/"}, "/", "search (zoomed)"),
		SearchWord:     b([]string{"*"}, "*/#", "search word under cursor"),
		SearchWordBack: b([]string{"#"}, "#", "search word backward"),

		ToggleTag:  b([]string{"space"}, "space", "check/uncheck tag"),
		RunChecked: b([]string{"enter"}, "enter", "run checked tags"),
		TagOptions: b([]string{"esc"}, "esc", "d D f a options (again: close)"),

		FetchBranches: b([]string{"ctrl+r"}, "ctrl+r", "fetch origin"),
		Base:          b([]string{"b"}, "b", "compare with another branch"),

		Close:      b([]string{"esc", "q"}, "esc", "close"),
		CopyReport: b([]string{"y"}, "y", "copy report as Markdown"),

		NextMatch: b([]string{"n"}, "n/N", "next/prev match"),
		PrevMatch: b([]string{"N"}, "N", "prev match"),

		Confirm: b([]string{"y"}, "y", "confirm"),
	}
}
