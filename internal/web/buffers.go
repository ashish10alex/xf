package web

import (
	"net/http"

	"github.com/ashish10alex/xf/internal/project"
)

// The source pane's buffers, as vim's: the files it has opened, which
// <leader>[ and <leader>] step through and <leader>b lists. The page keeps
// the list; the server keeps it while the project is open, so a reload and
// the project's other tabs have it, and says which of its files are still
// there and what their actions are now. Like vim's, the list starts empty
// each time xf opens the project: it is not kept in the session file, so
// files opened one after the other sit next to each other in it.

// maxBuffers is how many of the source pane's buffers are kept, the
// newest, and how many files one bufferFiles asks about.
const maxBuffers = 200

// buffer is a file the source pane has opened: its number, kept while it
// is listed, and the action it last showed, none for a file without one.
type buffer struct {
	N      int              `json:"n"`
	File   string           `json:"file"`
	Action project.ActionID `json:"action,omitempty"`
}

// buffersView is the source pane's buffers and the number the next one
// gets.
type buffersView struct {
	List []buffer `json:"list"`
	Next int      `json:"next"`
}

// buffers keeps the source pane's buffers, for when a page next shows the
// project while it is open.
func (p *projectState) buffers(w http.ResponseWriter, r *http.Request) {
	var req buffersView
	if !readJSON(w, r, &req) {
		return
	}
	if len(req.List) > maxBuffers {
		req.List = req.List[len(req.List)-maxBuffers:]
	}
	p.mu.Lock()
	p.bufs = req
	p.mu.Unlock()
	writeJSON(w, okView{OK: true})
}

// bufferFile is a buffer's file as it is now: gone when the pane can no
// longer show it (deleted, renamed, not on this branch), and its actions,
// the main one first, as ActionsInFile orders them.
type bufferFile struct {
	File    string         `json:"file"`
	Gone    bool           `json:"gone,omitempty"`
	Actions []bufferAction `json:"actions"`
}

type bufferAction struct {
	ID   project.ActionID `json:"id"`
	Name string           `json:"name"`
}

// bufferFiles answers {files} with each file as it is now. It reads no
// git, so a step to the next buffer costs a stat.
func (p *projectState) bufferFiles(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Files []string `json:"files"`
	}
	if !readJSON(w, r, &req) {
		return
	}
	if len(req.Files) > maxBuffers {
		req.Files = req.Files[:maxBuffers]
	}
	g := p.graph()
	out := make([]bufferFile, 0, len(req.Files))
	for _, f := range req.Files {
		v := bufferFile{File: f, Actions: []bufferAction{}}
		if _, err := p.sourcePath(f); err != nil {
			v.Gone = true
		} else if g != nil {
			for _, a := range g.ActionsInFile(f) {
				v.Actions = append(v.Actions, bufferAction{ID: a.ID, Name: a.Target.Name})
			}
		}
		out = append(out, v)
	}
	writeJSON(w, struct {
		Files []bufferFile `json:"files"`
	}{out})
}
