package web

import (
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"
)

// The page log is what each page notes for debugging a page that stops
// answering: every key and where it went, draws that failed, uncaught
// errors, requests that failed or hang, focus moves, the event stream
// dropping. The page sends its lines every few seconds (diag.js), and
// they are appended to Config.PageLog, so they outlive the tab.

// pageLogMax is how big the log grows before it starts again, the last
// one kept beside it as <name>.1.
const pageLogMax = 4 << 20

type pageLog struct {
	mu   sync.Mutex
	path string
}

type pageLogLine struct {
	T    int64  `json:"t"` // when the page noted it, in ms since the epoch
	Text string `json:"text"`
}

type pageLogBody struct {
	Tab     string        `json:"tab"`
	Project string        `json:"project"`
	Lines   []pageLogLine `json:"lines"`
}

// write appends b's lines to the log, each with its time, tab and project.
func (l *pageLog) write(b pageLogBody) error {
	if l.path == "" || len(b.Lines) == 0 {
		return nil
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	if err := os.MkdirAll(filepath.Dir(l.path), 0o755); err != nil {
		return err
	}
	if fi, err := os.Stat(l.path); err == nil && fi.Size() > pageLogMax {
		_ = os.Rename(l.path, l.path+".1")
	}
	f, err := os.OpenFile(l.path, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o600)
	if err != nil {
		return err
	}
	tab := strings.TrimPrefix(b.Tab, "xf-tab-")
	if len(tab) > 6 {
		tab = tab[:6]
	}
	if tab == "" {
		tab = "-"
	}
	project := b.Project
	if project == "" {
		project = "-"
	}
	var sb strings.Builder
	for _, ln := range b.Lines {
		at := time.UnixMilli(ln.T).Format("2006-01-02 15:04:05.000")
		text := strings.ReplaceAll(ln.Text, "\n", "\n    ") // a stack stays under its line
		fmt.Fprintf(&sb, "%s %s %s %s\n", at, tab, project, text)
	}
	_, err = f.WriteString(sb.String())
	if cerr := f.Close(); err == nil {
		err = cerr
	}
	return err
}

// postPageLog takes a page's lines, answering with where they are kept.
func (s *Server) postPageLog(w http.ResponseWriter, r *http.Request) {
	var b pageLogBody
	if !readJSON(w, r, &b) {
		return
	}
	if err := s.pageLog.write(b); err != nil {
		writeError(w, err)
		return
	}
	writeJSON(w, map[string]string{"path": s.pageLog.path})
}
