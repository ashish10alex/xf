package lsp

import (
	"net/url"
	"runtime"
	"strings"

	"github.com/ashish10alex/xf/internal/project"
)

// The subset of LSP types the server uses.

type Position struct {
	Line      int `json:"line"`
	Character int `json:"character"`
}

type Range struct {
	Start Position `json:"start"`
	End   Position `json:"end"`
}

type Location struct {
	URI   string `json:"uri"`
	Range Range  `json:"range"`
}

type TextDocumentIdentifier struct {
	URI string `json:"uri"`
}

type TextDocumentItem struct {
	URI  string `json:"uri"`
	Text string `json:"text"`
}

type DidOpenTextDocumentParams struct {
	TextDocument TextDocumentItem `json:"textDocument"`
}

type DidChangeTextDocumentParams struct {
	TextDocument   TextDocumentIdentifier `json:"textDocument"`
	ContentChanges []struct {
		Text string `json:"text"`
	} `json:"contentChanges"`
}

type DidCloseTextDocumentParams struct {
	TextDocument TextDocumentIdentifier `json:"textDocument"`
}

type DocumentFormattingParams struct {
	TextDocument TextDocumentIdentifier `json:"textDocument"`
}

type TextDocumentPositionParams struct {
	TextDocument TextDocumentIdentifier `json:"textDocument"`
	Position     Position               `json:"position"`
}

type InitializeParams struct {
	RootURI          string `json:"rootUri"`
	WorkspaceFolders []struct {
		URI string `json:"uri"`
	} `json:"workspaceFolders"`
	Capabilities struct {
		General struct {
			PositionEncodings []string `json:"positionEncodings"`
		} `json:"general"`
		Window struct {
			ShowDocument struct {
				Support bool `json:"support"`
			} `json:"showDocument"`
		} `json:"window"`
	} `json:"capabilities"`
}

type InitializeResult struct {
	Capabilities ServerCapabilities `json:"capabilities"`
	ServerInfo   struct {
		Name string `json:"name"`
	} `json:"serverInfo"`
}

type ServerCapabilities struct {
	PositionEncoding       string `json:"positionEncoding"`
	TextDocumentSync       int    `json:"textDocumentSync"`
	DefinitionProvider     bool   `json:"definitionProvider"`
	DocumentSymbolProvider bool   `json:"documentSymbolProvider"`
	HoverProvider          bool   `json:"hoverProvider"`
	// CompletionProvider is a pointer so it is left out when nil.
	CompletionProvider *CompletionOptions `json:"completionProvider,omitempty"`
	CodeActionProvider bool               `json:"codeActionProvider"`
	// DocumentFormattingProvider is set for a Dataform project when the
	// server can format (Config.Format).
	DocumentFormattingProvider bool `json:"documentFormattingProvider,omitempty"`
	// Experimental says which xf extensions the server speaks, so the
	// nvim plugin can tell an out-of-date xf lsp from a working one.
	Experimental *Experimental `json:"experimental,omitempty"`
}

type Experimental struct {
	// XfActiveFile is set when the server takes xf/activeFile.
	XfActiveFile bool `json:"xfActiveFile"`
	// XfDbt is set when the server serves dbt projects, not only passes
	// their active file on.
	XfDbt bool `json:"xfDbt"`
}

type CompletionOptions struct {
	TriggerCharacters []string `json:"triggerCharacters"`
}

type CompletionItem struct {
	Label      string    `json:"label"`
	Kind       int       `json:"kind"`
	Detail     string    `json:"detail,omitempty"`
	FilterText string    `json:"filterText,omitempty"`
	TextEdit   *TextEdit `json:"textEdit,omitempty"`
}

type CompletionList struct {
	IsIncomplete bool             `json:"isIncomplete"`
	Items        []CompletionItem `json:"items"`
}

// Completion item kinds.
const (
	completionKindFunction   = 3
	completionKindProperty   = 10
	completionKindKeyword    = 14
	completionKindReference  = 18
	completionKindEnumMember = 20
)

type TextEdit struct {
	Range   Range  `json:"range"`
	NewText string `json:"newText"`
}

type Diagnostic struct {
	Range    Range           `json:"range"`
	Severity int             `json:"severity"`
	Source   string          `json:"source,omitempty"`
	Message  string          `json:"message"`
	Data     *DiagnosticData `json:"data,omitempty"`
}

// DiagnosticData rides along with a diagnostic and comes back in code action
// requests.
type DiagnosticData struct {
	// Fix replaces the diagnostic's range ("Did you mean X?").
	Fix string `json:"fix,omitempty"`
}

// Diagnostic severities.
const (
	severityError = 1
	severityHint  = 4
)

type PublishDiagnosticsParams struct {
	URI         string       `json:"uri"`
	Diagnostics []Diagnostic `json:"diagnostics"`
}

type CodeActionParams struct {
	TextDocument TextDocumentIdentifier `json:"textDocument"`
	Range        Range                  `json:"range"`
	Context      struct {
		Diagnostics []Diagnostic `json:"diagnostics"`
	} `json:"context"`
}

type CodeAction struct {
	Title       string         `json:"title"`
	Kind        string         `json:"kind"`
	Diagnostics []Diagnostic   `json:"diagnostics,omitempty"`
	IsPreferred bool           `json:"isPreferred,omitempty"`
	Edit        *WorkspaceEdit `json:"edit,omitempty"`
}

type WorkspaceEdit struct {
	Changes map[string][]TextEdit `json:"changes"`
}

type Hover struct {
	Contents MarkupContent `json:"contents"`
	Range    *Range        `json:"range,omitempty"`
}

type MarkupContent struct {
	Kind  string `json:"kind"`
	Value string `json:"value"`
}

type DocumentSymbolParams struct {
	TextDocument TextDocumentIdentifier `json:"textDocument"`
}

type DocumentSymbol struct {
	Name           string           `json:"name"`
	Detail         string           `json:"detail,omitempty"`
	Kind           int              `json:"kind"`
	Range          Range            `json:"range"`
	SelectionRange Range            `json:"selectionRange"`
	Children       []DocumentSymbol `json:"children,omitempty"`
}

// Symbol kinds.
const (
	symbolKindFunction = 12
	symbolKindVariable = 13
	symbolKindStruct   = 23
)

// Text document sync kinds.
const syncFull = 1

// Position encodings.
const (
	encodingUTF8  = "utf-8"
	encodingUTF16 = "utf-16"
)

// ShowDocumentParams are window/showDocument's, which the server sends
// the editor.
type ShowDocumentParams struct {
	URI       string `json:"uri"`
	TakeFocus bool   `json:"takeFocus,omitempty"`
	Selection *Range `json:"selection,omitempty"`
}

type ShowDocumentResult struct {
	Success bool `json:"success"`
}

type ShowMessageParams struct {
	Type    int    `json:"type"`
	Message string `json:"message"`
}

// Message types for window/showMessage.
const messageWarning = 2

// goos is the OS whose paths uriPath and pathURI make; tests change it.
var goos = runtime.GOOS

// uriPath turns a file:// URI into a local path. It returns "" for other
// schemes. On Windows, file:///C:/x (or VS Code's file:///c%3A/x) is C:\x,
// with the drive letter upper case so every client's paths agree, and
// file://server/share/x is \\server\share\x.
func uriPath(uri string) string {
	u, err := url.Parse(uri)
	if err != nil || u.Scheme != "file" {
		return ""
	}
	p := u.Path
	if goos == "windows" {
		switch {
		case u.Host != "" && u.Host != "localhost":
			p = "//" + u.Host + p
		case len(p) >= 3 && p[0] == '/' && isDriveLetter(p[1]) && p[2] == ':':
			p = project.CanonicalDrive(p[1:])
		}
		return strings.ReplaceAll(p, "/", `\`)
	}
	return p
}

// pathURI is the file:// URI of a local path: file:///C:/x for C:\x on
// Windows, and file://server/share/x for \\server\share\x.
func pathURI(path string) string {
	if goos != "windows" {
		return (&url.URL{Scheme: "file", Path: path}).String()
	}
	p := strings.ReplaceAll(path, `\`, "/")
	if strings.HasPrefix(p, "//") {
		host, rest, _ := strings.Cut(p[2:], "/")
		return (&url.URL{Scheme: "file", Host: host, Path: "/" + rest}).String()
	}
	if len(p) >= 2 && isDriveLetter(p[0]) && p[1] == ':' {
		p = "/" + project.CanonicalDrive(p)
	}
	return (&url.URL{Scheme: "file", Path: p}).String()
}

func isDriveLetter(c byte) bool { return 'a' <= c && c <= 'z' || 'A' <= c && c <= 'Z' }
