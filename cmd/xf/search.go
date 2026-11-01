package main

import (
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"text/tabwriter"

	"github.com/ashish10alex/xf/internal/backend"
	"github.com/ashish10alex/xf/internal/frontend"
	"github.com/ashish10alex/xf/internal/project"
	"github.com/ashish10alex/xf/internal/session"
)

// pathList is a flag that may be given more than once.
type pathList []string

func (p *pathList) String() string     { return strings.Join(*p, ", ") }
func (p *pathList) Set(s string) error { *p = append(*p, s); return nil }

// runSearch is xf search: it finds the Dataform and dbt projects in
// folders and adds them to the projects the picker lists.
func runSearch(args []string) error {
	fs := flag.NewFlagSet("xf search", flag.ContinueOnError)
	var dirs pathList
	fs.Var(&dirs, "path", "a `dir` to search; may be given more than once, as may dirs after the flags (default: the working directory)")
	want := fs.String("backend", "", "look only for projects of this `tool`, "+strings.Join(backend.Names, " or ")+" (default: both; a folder that is both is Dataform's)")
	commandUsage(fs, "xf search [--path dir] [--backend tool] [dir…]", "Finds the Dataform and dbt projects in the folders, and the folders below them,\nand adds them to the projects xf, xf web and the picker list, so they can be\npicked before xf has opened them. Hidden folders, node_modules, dbt_packages,\ntarget and the folders inside a project are skipped. Projects xf already\nknows are left as they are.")
	// Flags may come after the dirs too: parse again after each.
	for {
		if help, err := parseArgs(fs, args); help || err != nil {
			return err
		}
		if fs.NArg() == 0 {
			break
		}
		dirs = append(dirs, fs.Arg(0))
		args = fs.Args()[1:]
	}
	if len(dirs) == 0 {
		dirs = pathList{"."}
	}
	dir := sessionDir()
	if dir == "" {
		return errors.New("no home folder to keep the projects in")
	}
	home, _ := os.UserHomeDir()
	found, err := searchProjects(home, dirs, *want)
	if err != nil {
		return err
	}
	return addFound(os.Stdout, dir, home, found)
}

// searchProjects searches each of dirs (~ is the home folder) for want's
// projects, listing a project found from more than one of them once.
func searchProjects(home string, dirs []string, want string) ([]backend.Found, error) {
	var out []backend.Found
	seen := map[string]bool{}
	for _, d := range dirs {
		abs, err := filepath.Abs(frontend.ExpandPath(home, d))
		if err != nil {
			return nil, err
		}
		found, err := backend.Search(abs, want)
		if err != nil {
			return nil, err
		}
		for _, f := range found {
			if real := project.RealPath(f.Root); !seen[real] {
				seen[real] = true
				out = append(out, f)
			}
		}
	}
	return out, nil
}

// addFound adds the projects found to the sessions in dir and prints each,
// with whether it was added, then how many there were.
func addFound(w io.Writer, dir, home string, found []backend.Found) error {
	if len(found) == 0 {
		_, err := fmt.Fprintln(w, "no Dataform or dbt projects found")
		return err
	}
	tw := tabwriter.NewWriter(w, 0, 0, 2, ' ', 0)
	added := 0
	for _, f := range found {
		ok, err := session.Add(dir, f.Root, f.Backend)
		if err != nil {
			_ = tw.Flush()
			return fmt.Errorf("adding %s: %w", f.Root, err)
		}
		state := "already known"
		if ok {
			state = "added"
			added++
		}
		_, _ = fmt.Fprintf(tw, "%s\t%s\t%s\t%s\n", filepath.Base(f.Root), f.Backend, frontend.ShortPath(home, f.Root), state)
	}
	if err := tw.Flush(); err != nil {
		return err
	}
	_, err := fmt.Fprintf(w, "\n%s found: %d added, %d already known\n", plural(len(found), "project"), added, len(found)-added)
	return err
}

// plural is n and noun, with an s unless n is 1.
func plural(n int, noun string) string {
	if n == 1 {
		return "1 " + noun
	}
	return fmt.Sprintf("%d %ss", n, noun)
}
