package commanduse

import (
	"os"
	"path/filepath"
	"slices"
	"testing"
	"time"
)

func TestAddCountsAndKeepsOtherWriters(t *testing.T) {
	path := filepath.Join(t.TempDir(), "state", "commands.json")
	clock := time.Date(2026, 10, 16, 9, 0, 0, 0, time.UTC)
	a, b := Open(path), Open(path)
	a.now = func() time.Time { return clock }
	b.now = a.now

	if got := a.Load(); len(got) != 0 {
		t.Fatalf("Load of no file = %v, want empty", got)
	}
	if _, err := a.Add("Run"); err != nil {
		t.Fatal(err)
	}
	if _, err := b.Add("Run"); err != nil { // the other xf
		t.Fatal(err)
	}
	clock = clock.Add(time.Minute)
	uses, err := a.Add("Check: compile")
	if err != nil {
		t.Fatal(err)
	}
	if uses["Run"].Count != 2 || uses["Check: compile"].Count != 1 || !uses["Check: compile"].Last.Equal(clock) {
		t.Fatalf("uses = %v", uses)
	}
	if got := b.Load(); got["Run"].Count != 2 {
		t.Fatalf("Load = %v, want Run twice", got)
	}
}

func TestBadFileIsNoCounts(t *testing.T) {
	path := filepath.Join(t.TempDir(), "commands.json")
	if err := os.WriteFile(path, []byte("{nope"), 0o644); err != nil {
		t.Fatal(err)
	}
	s := Open(path)
	if got := s.Load(); len(got) != 0 {
		t.Fatalf("Load = %v, want empty", got)
	}
	uses, err := s.Add("Run")
	if err != nil || uses["Run"].Count != 1 {
		t.Fatalf("Add = %v, %v", uses, err)
	}
}

func TestNilStore(t *testing.T) {
	var s *Store
	if len(s.Load()) != 0 {
		t.Fatal("nil Load not empty")
	}
	if _, err := s.Add("Run"); err != nil {
		t.Fatal(err)
	}
}

func TestCompareOrdersMostUsedThenLatest(t *testing.T) {
	t0 := time.Date(2026, 10, 16, 9, 0, 0, 0, time.UTC)
	uses := map[string]Use{
		"often":  {Count: 5, Last: t0},
		"lately": {Count: 2, Last: t0.Add(time.Hour)},
		"once":   {Count: 2, Last: t0},
	}
	titles := []string{"never", "once", "often", "never too", "lately"}
	slices.SortStableFunc(titles, func(a, b string) int { return Compare(uses[a], uses[b]) })
	want := []string{"often", "lately", "once", "never", "never too"}
	if !slices.Equal(titles, want) {
		t.Fatalf("order = %v, want %v", titles, want)
	}
}
