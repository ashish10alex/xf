// Package commanduse counts how often the palette's commands are done, so
// the palette can list the most used first, as VS Code's does. The counts
// are the user's, for every project and both the TUI and xf web: one JSON
// file beside the sessions (see package session), keyed by the command's
// title.
package commanduse

import (
	"cmp"
	"encoding/json"
	"os"
	"path/filepath"
	"sync"
	"time"
)

// Use is how often a command was done, and when last.
type Use struct {
	Count int       `json:"count"`
	Last  time.Time `json:"last"`
}

// Compare orders the more used first: the higher count, then the later
// use. Commands never used compare equal, so a stable sort keeps them in
// the palette's own order.
func Compare(a, b Use) int {
	if c := cmp.Compare(b.Count, a.Count); c != 0 {
		return c
	}
	return b.Last.Compare(a.Last)
}

// Store keeps the counts in a file. A nil Store keeps nothing: Load is
// empty and Add does nothing.
type Store struct {
	path string
	mu   sync.Mutex
	now  func() time.Time
}

// Open is the store at path, which need not exist yet.
func Open(path string) *Store {
	return &Store{path: path, now: time.Now}
}

// Path is the file the counts are kept in, given the sessions' directory:
// commands.json beside it.
func Path(sessionDir string) string {
	return filepath.Join(filepath.Dir(sessionDir), "commands.json")
}

// Load reads the counts. They are only a convenience, so a file that is
// missing or unreadable is no counts.
func (s *Store) Load() map[string]Use {
	if s == nil {
		return map[string]Use{}
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.loadLocked()
}

func (s *Store) loadLocked() map[string]Use {
	uses := map[string]Use{}
	data, err := os.ReadFile(s.path)
	if err != nil || json.Unmarshal(data, &uses) != nil {
		return map[string]Use{}
	}
	return uses
}

// Add counts a use of the command titled title, and returns the counts
// with it. The file is read again first, so uses the other xf (the TUI or
// xf web) added since are kept.
func (s *Store) Add(title string) (map[string]Use, error) {
	if s == nil {
		return map[string]Use{}, nil
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	uses := s.loadLocked()
	u := uses[title]
	u.Count++
	u.Last = s.now().UTC()
	uses[title] = u
	data, err := json.MarshalIndent(uses, "", "  ")
	if err != nil {
		return uses, err
	}
	if err := os.MkdirAll(filepath.Dir(s.path), 0o755); err != nil {
		return uses, err
	}
	// Write a file beside it and rename, so a reader never sees half.
	tmp, err := os.CreateTemp(filepath.Dir(s.path), ".commands-*.json")
	if err != nil {
		return uses, err
	}
	_, werr := tmp.Write(data)
	cerr := tmp.Close()
	if err := cmp.Or(werr, cerr); err != nil {
		os.Remove(tmp.Name())
		return uses, err
	}
	if err := os.Rename(tmp.Name(), s.path); err != nil {
		os.Remove(tmp.Name())
		return uses, err
	}
	return uses, nil
}
