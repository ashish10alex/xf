package main

import (
	"bufio"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"

	"github.com/ashish10alex/xf/internal/agentskills"
)

const skillsUsage = `usage: xf skills [status | install | update | uninstall] [flags]

Installs xf's Agent Skills (xf-checks and xf-run) for the coding agents on
this machine: Claude Code, Codex, Antigravity and Cursor. They teach an agent
to run xf cost, xf impact and xf run with the flags of this xf.

  xf skills            each agent's skills folder and what xf's skills there are
  xf skills install    install them for the agents found (or --agent), after
                         asking at a terminal; again after an update refreshes them
  xf skills update     update only the skills an older xf installed; xf update
                         runs it for you
  xf skills uninstall  remove the skills xf installed

xf marks the SKILL.md files it writes, and replaces or removes only those,
and only when nobody has edited them since (--force overrides). Skills
another tool installed, such as npx skills add, are left to that tool.

flags:
`

// skillsFlags are xf skills' flags.
type skillsFlags struct {
	agent          string
	project        bool
	yes, force, qt bool
}

// skillsEnv is what xf skills reads of the machine; tests replace it.
type skillsEnv struct {
	home        string
	getenv      func(string) string
	projectRoot func() (string, error)
	in          io.Reader
	interactive bool
}

func runSkills(args []string) error {
	home, err := os.UserHomeDir()
	if err != nil {
		return fmt.Errorf("can't find your home folder: %w", err)
	}
	env := skillsEnv{home: home, getenv: os.Getenv, projectRoot: gitRoot, in: os.Stdin, interactive: interactive()}
	return skills(os.Stdout, env, args)
}

func skills(w io.Writer, env skillsEnv, args []string) error {
	sub := "status"
	if len(args) > 0 && !strings.HasPrefix(args[0], "-") {
		sub, args = args[0], args[1:]
	}
	fs := flag.NewFlagSet("xf skills", flag.ContinueOnError)
	var f skillsFlags
	fs.StringVar(&f.agent, "agent", "", "the agents, comma-separated: claude, codex, antigravity, cursor (default: those found)")
	fs.BoolVar(&f.project, "project", false, "the project's skills folders (.claude/skills, .agents/skills, .cursor/skills at the git root), to commit for the team, instead of your own")
	fs.BoolVar(&f.yes, "yes", false, "install without asking")
	fs.BoolVar(&f.force, "force", false, "also replace or remove skills edited since xf installed them, and install over another tool's")
	fs.BoolVar(&f.qt, "quiet", false, "say only what changed")
	fs.Usage = func() {
		_, _ = io.WriteString(fs.Output(), skillsUsage)
		printFlags(fs.Output(), fs)
	}
	if help, err := parseArgs(fs, args); help || err != nil {
		return err
	}
	if fs.NArg() > 0 {
		return fmt.Errorf("unexpected arguments: %v (see xf skills --help)", fs.Args())
	}
	all := agentskills.Agents(env.home, env.getenv)
	picked, err := pickAgents(all, f.agent)
	if err != nil {
		return err
	}
	root := ""
	if f.project {
		if root, err = env.projectRoot(); err != nil {
			return err
		}
	}
	switch sub {
	case "status":
		if picked == nil {
			picked = shownAgents(all, root)
		}
		return skillsStatus(w, env, picked, root)
	case "install":
		if picked == nil {
			for _, a := range all {
				if a.Installed() {
					picked = append(picked, a)
				}
			}
			if len(picked) == 0 {
				return fmt.Errorf("found no coding agent (looked for %s); pass --agent claude, codex, antigravity or cursor", strings.Join(agentHomes(env, all), ", "))
			}
		}
		return skillsInstall(w, env, f, picked, root)
	case "update", "uninstall":
		if picked == nil {
			picked = all
		}
		return skillsChange(w, env, f, sub, picked, root)
	}
	return fmt.Errorf("unknown xf skills command %q (see xf skills --help)", sub)
}

// pickAgents reads --agent; nil means it wasn't given.
func pickAgents(all []agentskills.Agent, list string) ([]agentskills.Agent, error) {
	if list == "" {
		return nil, nil
	}
	var out []agentskills.Agent
	for id := range strings.SplitSeq(list, ",") {
		id = strings.TrimSpace(strings.ToLower(id))
		if id == "gemini" {
			id = "antigravity"
		}
		i := slices.IndexFunc(all, func(a agentskills.Agent) bool { return a.ID == id })
		if i < 0 {
			return nil, fmt.Errorf("--agent %s: not one of claude, codex, antigravity, cursor", id)
		}
		if !slices.ContainsFunc(out, func(a agentskills.Agent) bool { return a.ID == id }) {
			out = append(out, all[i])
		}
	}
	return out, nil
}

// shownAgents are the agents xf skills lists: those installed, and any
// other whose folder holds one of xf's skills.
func shownAgents(all []agentskills.Agent, root string) []agentskills.Agent {
	var out []agentskills.Agent
	for _, a := range all {
		has := slices.ContainsFunc(agentskills.Check(skillsDir(a, root)), func(s agentskills.Status) bool { return s.State != agentskills.Missing })
		if a.Installed() || has {
			out = append(out, a)
		}
	}
	return out
}

// skillsDir is the folder a's skills go in: its own, or the project's
// under root when root is set.
func skillsDir(a agentskills.Agent, root string) string {
	if root != "" {
		return filepath.Join(root, a.ProjectDir)
	}
	return a.UserDir
}

// target is a skills folder and the agents that read it: Codex and
// Antigravity share a project's .agents/skills.
type target struct {
	dir    string
	agents []string
}

func targets(agents []agentskills.Agent, root string) []target {
	var out []target
	for _, a := range agents {
		dir := skillsDir(a, root)
		if i := slices.IndexFunc(out, func(t target) bool { return t.dir == dir }); i >= 0 {
			out[i].agents = append(out[i].agents, a.Name)
			continue
		}
		out = append(out, target{dir: dir, agents: []string{a.Name}})
	}
	return out
}

func skillsStatus(w io.Writer, env skillsEnv, agents []agentskills.Agent, root string) error {
	if len(agents) == 0 {
		_, _ = fmt.Fprintf(w, "found no coding agent (looked for %s)\n", strings.Join(agentHomes(env, agentskills.Agents(env.home, env.getenv)), ", "))
		return nil
	}
	for _, t := range targets(agents, root) {
		_, _ = fmt.Fprintf(w, "%s  %s\n", strings.Join(t.agents, ", "), tilde(env, t.dir))
		for _, st := range agentskills.Check(t.dir) {
			_, _ = fmt.Fprintf(w, "  %-10s %s%s\n", st.Skill.Name, st.State, statusHint(st.State))
		}
	}
	for _, a := range agents {
		if root == "" && a.HasPlugin() {
			_, _ = fmt.Fprintf(w, "%s also has xf's plugin (xf@xf), which carries the same skills; /plugin updates it\n", a.Name)
		}
	}
	return nil
}

func statusHint(s agentskills.State) string {
	return hintFor(s, "xf skills install --force replaces it")
}

// hintFor says what to do about a skill in state s; force is what --force
// would do with an edited one.
func hintFor(s agentskills.State, force string) string {
	switch s {
	case agentskills.Missing:
		return ": xf skills install"
	case agentskills.Outdated:
		return ": xf skills update"
	case agentskills.Edited:
		return " (" + force + ")"
	case agentskills.Foreign, agentskills.Linked:
		return " (update it with that tool, such as npx skills update)"
	}
	return ""
}

func skillsInstall(w io.Writer, env skillsEnv, f skillsFlags, agents []agentskills.Agent, root string) error {
	var keep []agentskills.Agent
	for _, a := range agents {
		if root == "" && !f.force && a.HasPlugin() {
			_, _ = fmt.Fprintf(w, "skipping %s: it has xf's plugin (xf@xf), which carries the same skills; /plugin updates it (--force installs them as well)\n", a.Name)
			continue
		}
		keep = append(keep, a)
	}
	ts := targets(keep, root)
	if len(ts) == 0 {
		return nil
	}
	if env.interactive && !f.yes {
		var names []string
		for _, s := range agentskills.Bundled() {
			names = append(names, s.Name)
		}
		_, _ = fmt.Fprintf(w, "Install xf's skills (%s) for:\n", strings.Join(names, ", "))
		for _, t := range ts {
			_, _ = fmt.Fprintf(w, "  %-14s %s\n", strings.Join(t.agents, ", "), tilde(env, t.dir))
		}
		_, _ = io.WriteString(w, "Install? [Y/n] ")
		answer, _ := bufio.NewReader(env.in).ReadString('\n')
		if a := strings.ToLower(strings.TrimSpace(answer)); a != "" && a != "y" && a != "yes" {
			_, _ = io.WriteString(w, "nothing installed\n")
			return nil
		}
	}
	var errs []error
	for _, t := range ts {
		acts, err := agentskills.Install(t.dir, f.force)
		if err != nil {
			errs = append(errs, err)
		}
		report(w, env, acts, "installed", "xf skills install --force replaces it", f.qt)
	}
	return errors.Join(errs...)
}

func skillsChange(w io.Writer, env skillsEnv, f skillsFlags, sub string, agents []agentskills.Agent, root string) error {
	var errs []error
	said := false
	for _, t := range targets(agents, root) {
		var acts []agentskills.Action
		var err error
		if sub == "update" {
			acts, err = agentskills.Update(t.dir)
		} else {
			acts, err = agentskills.Uninstall(t.dir, f.force)
		}
		if err != nil {
			errs = append(errs, err)
		}
		// Only folders with xf's skills in them are worth a line.
		acts = slices.DeleteFunc(acts, func(a agentskills.Action) bool {
			return a.State == agentskills.Missing || a.State == agentskills.Foreign || a.State == agentskills.Linked
		})
		if len(acts) > 0 {
			said = true
		}
		if sub == "update" {
			report(w, env, acts, "updated", "xf skills install --force replaces it", f.qt)
		} else {
			report(w, env, acts, "removed", "xf skills uninstall --force removes it", f.qt)
		}
	}
	if !said && !f.qt {
		_, _ = fmt.Fprintf(w, "no skills xf installed to %s\n", sub)
	}
	return errors.Join(errs...)
}

// report says what was done with each skill, verb its word for done and
// force what --force would do with an edited one; quiet leaves out the
// ones that were already as they should be.
func report(w io.Writer, env skillsEnv, acts []agentskills.Action, verb, force string, quiet bool) {
	for _, a := range acts {
		where := tilde(env, filepath.Dir(a.Path))
		switch {
		case a.Done:
			_, _ = fmt.Fprintf(w, "%s %s\n", verb, where)
		case a.State == agentskills.Current:
			if !quiet {
				_, _ = fmt.Fprintf(w, "%s is up to date\n", where)
			}
		case a.State == agentskills.Missing:
		default:
			_, _ = fmt.Fprintf(w, "left %s: %s%s\n", where, a.State, hintFor(a.State, force))
		}
	}
}

// tilde writes a path under the home folder with ~.
func tilde(env skillsEnv, p string) string {
	if rel, err := filepath.Rel(env.home, p); err == nil && !strings.HasPrefix(rel, "..") && !filepath.IsAbs(rel) {
		return filepath.Join("~", rel)
	}
	return p
}

func agentHomes(env skillsEnv, all []agentskills.Agent) []string {
	var out []string
	for _, a := range all {
		out = append(out, tilde(env, a.Home))
	}
	return out
}

// gitRoot is the root of the git repository the working directory is in,
// or the working directory outside one.
func gitRoot() (string, error) {
	out, err := exec.Command("git", "rev-parse", "--show-toplevel").Output()
	if err == nil {
		return filepath.FromSlash(strings.TrimSpace(string(out))), nil
	}
	return os.Getwd()
}

// refreshSkills runs the xf that xf update installed, at exe, to update
// the skills an older xf installed: this xf holds the old ones.
func refreshSkills(w io.Writer, exe string) {
	if _, err := os.Stat(exe); err != nil {
		// Homebrew removes the old version's folder; its link on PATH
		// leads to the new one.
		if exe, err = exec.LookPath("xf"); err != nil {
			return
		}
	}
	c := exec.Command(exe, "skills", "update", "--quiet")
	c.Stdout, c.Stderr = w, os.Stderr
	if err := c.Run(); err != nil {
		_, _ = fmt.Fprintf(w, "couldn't update xf's agent skills (%v); run: xf skills update\n", err)
	}
}

// skillsNotice is xf web's notice when an older xf installed skills that
// nobody has edited since, or "".
func skillsNotice() string {
	home, err := os.UserHomeDir()
	if err != nil {
		return ""
	}
	if len(agentskills.Stale(agentskills.Agents(home, os.Getenv))) == 0 {
		return ""
	}
	return "your agent skills are from an older xf: run xf skills update"
}
