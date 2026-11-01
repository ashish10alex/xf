package navigate

import (
	"io/fs"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/ashish10alex/xf/internal/backend/dataform"
	"github.com/ashish10alex/xf/internal/backend/dbt"
	"github.com/ashish10alex/xf/internal/project"
)

// Files saved on Windows often have CRLF line endings: navigating one gives
// the same answers as the LF file, at the same places, with no stray \r.
func TestCRLFSources(t *testing.T) {
	for _, c := range []struct {
		root, fixture string
		dbt           bool
	}{
		{"examples/dataform", "testdata/examples/dataform.json", false},
		{"examples/dbt", "testdata/examples/dbt-core.json", true},
		{"testdata/projects/navigation", "", false},
	} {
		t.Run(c.root, func(t *testing.T) {
			root := repoPath(t, c.root)
			g := crlfGraph(t, c.fixture, c.dbt)
			p := Project{Root: root, Graph: g, Dbt: c.dbt}
			// The whole project saved with CRLF, so jumps land in CRLF
			// files too (a source's YAML, say).
			q := Project{Root: crlfCopy(t, root), Graph: g, Dbt: c.dbt}
			var jumps, yamlJumps int
			_ = filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
				if err != nil || d.IsDir() {
					return err
				}
				switch filepath.Ext(path) {
				case ".sqlx", ".js", ".sql", ".yml":
				default:
					return nil
				}
				lf, err := os.ReadFile(path)
				if err != nil {
					t.Fatal(err)
				}
				if strings.Contains(string(lf), "\r") {
					t.Fatalf("%s already has CRLF line endings", path)
				}
				j, y := checkCRLF(t, p, q, path, lf, c.dbt)
				jumps, yamlJumps = jumps+j, yamlJumps+y
				return nil
			})
			if jumps == 0 || (c.dbt && yamlJumps == 0) {
				t.Errorf("only %d definitions found (%d in YAML): the comparison checked nothing", jumps, yamlJumps)
			}
		})
	}
}

// checkCRLF compares what navigate says about file in p (LF) and in q (its
// CRLF copy), at every offset. It returns how many offsets jump somewhere,
// and how many of those land in a YAML file.
func checkCRLF(t *testing.T, p, q Project, file string, lf []byte, isDbt bool) (jumps, yamlJumps int) {
	t.Helper()
	crlf := []byte(strings.ReplaceAll(string(lf), "\n", "\r\n"))
	// toLF maps an offset in crlf to the same place in lf.
	toLF := func(off int) int { return off - strings.Count(string(crlf[:min(off, len(crlf))]), "\r") }
	rel, _ := filepath.Rel(p.Root, file)
	qfile := filepath.Join(q.Root, rel)
	// relLocs makes locations relative to their project, to compare them.
	relLocs := func(root string, locs []Location) []Location {
		for i := range locs {
			locs[i].File, _ = filepath.Rel(root, locs[i].File)
		}
		return locs
	}

	symbols := DocumentSymbols
	if isDbt {
		symbols = DbtDocumentSymbols
	}
	if want, got := symbols(file, lf), mapSymbols(symbols(file, crlf), toLF); !reflect.DeepEqual(got, want) {
		t.Errorf("%s: symbols differ with CRLF:\n got %+v\nwant %+v", rel, got, want)
	}
	if filepath.Ext(file) == ".sqlx" {
		if !reflect.DeepEqual(mapRegions(Regions(crlf), toLF), Regions(lf)) {
			t.Errorf("%s: regions differ with CRLF", rel)
		}
	}
	crlfOff := 0
	for off := 0; off <= len(lf); off++ {
		if off > 0 && lf[off-1] == '\n' {
			crlfOff++ // the \r before that \n
		}
		at := off + crlfOff
		w, g := relLocs(p.Root, Definition(p, file, lf, off)), relLocs(q.Root, Definition(q, qfile, crlf, at))
		if !reflect.DeepEqual(g, w) {
			t.Errorf("%s:%d: definition %+v with CRLF, %+v with LF", rel, off, g, w)
			return
		}
		if len(w) > 0 {
			jumps++
			if filepath.Ext(w[0].File) == ".yml" {
				yamlJumps++
			}
		}
		wc, gc := Completions(p, file, lf, off), Completions(q, qfile, crlf, at)
		for i := range gc {
			gc[i].Start = toLF(gc[i].Start)
		}
		if !reflect.DeepEqual(gc, wc) {
			t.Errorf("%s:%d: completions differ with CRLF", rel, off)
			return
		}
	}
	return jumps, yamlJumps
}

// mapSymbols maps symbols' offsets in a CRLF file to the LF file's.
func mapSymbols(syms []Symbol, toLF func(int) int) []Symbol {
	for i := range syms {
		s := &syms[i]
		s.Start, s.End, s.SelStart, s.SelEnd = toLF(s.Start), toLF(s.End), toLF(s.SelStart), toLF(s.SelEnd)
		s.Children = mapSymbols(s.Children, toLF)
	}
	return syms
}

// mapRegions maps regions of a CRLF file to the LF file's: the pieces'
// offsets, and their text without the \r (Dataform keeps it, so it lines up
// with the compiled SQL).
func mapRegions(r SQLXRegions, toLF func(int) int) SQLXRegions {
	mapRegion := func(rg Region) Region {
		for i := range rg.Pieces {
			p := &rg.Pieces[i]
			p.Start, p.End, p.Text = toLF(p.Start), toLF(p.End), strings.ReplaceAll(p.Text, "\r", "")
		}
		return rg
	}
	r.Main = mapRegion(r.Main)
	for i := range r.PreOps {
		r.PreOps[i] = mapRegion(r.PreOps[i])
	}
	for i := range r.PostOps {
		r.PostOps[i] = mapRegion(r.PostOps[i])
	}
	if r.ConfigStart >= 0 {
		r.ConfigStart, r.ConfigEnd = toLF(r.ConfigStart), toLF(r.ConfigEnd)
	}
	return r
}

// crlfCopy copies the project at root with every text file's line endings
// turned into CRLF.
func crlfCopy(t *testing.T, root string) string {
	t.Helper()
	dest := t.TempDir()
	err := filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel, _ := filepath.Rel(root, path)
		out := filepath.Join(dest, rel)
		if d.IsDir() {
			return os.MkdirAll(out, 0o755)
		}
		data, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		return os.WriteFile(out, []byte(strings.ReplaceAll(string(data), "\n", "\r\n")), 0o644)
	})
	if err != nil {
		t.Fatal(err)
	}
	return dest
}

func repoPath(t *testing.T, rel string) string {
	t.Helper()
	p, err := filepath.Abs(filepath.Join("..", "..", filepath.FromSlash(rel)))
	if err != nil {
		t.Fatal(err)
	}
	return p
}

func crlfGraph(t *testing.T, fixture string, isDbt bool) *project.Graph {
	t.Helper()
	if fixture == "" {
		return nil
	}
	data, err := os.ReadFile(repoPath(t, fixture))
	if err != nil {
		t.Fatal(err)
	}
	var g *project.Graph
	if isDbt {
		g, err = (&dbt.Backend{}).Parse(data)
	} else {
		g, err = dataform.ParseCompileJSON(data)
	}
	if err != nil {
		t.Fatal(err)
	}
	return g
}
