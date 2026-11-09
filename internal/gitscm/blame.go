package gitscm

import (
	"bufio"
	"context"
	"io"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/ashish10alex/xf/internal/backend/cmdrun"
)

// BlameCommit is a commit as a file's blame names it.
type BlameCommit struct {
	Author  string `json:"author"`
	Mail    string `json:"mail,omitempty"`
	Time    int64  `json:"time"` // the author's, in Unix seconds
	Summary string `json:"summary"`
}

// Blame is the commit each line of a file last changed in.
type Blame struct {
	// Lines are the commits' SHAs, a line's at its index; a line not
	// committed yet has "".
	Lines []string `json:"lines"`
	// Commits are the commits Lines names, by SHA.
	Commits map[string]BlameCommit `json:"commits"`
	// You is the mail git commits as here, so a page can say "You".
	You string `json:"you,omitempty"`
}

// BlameOf is git blame of the file at path with text as its contents, so
// lines changed and not committed, saved or not, are told apart. A file
// git doesn't track is an error.
func BlameOf(ctx context.Context, path string, text []byte) (Blame, error) {
	dir, name := filepath.Dir(path), filepath.Base(path)
	cmd := cmdrun.Git(ctx, dir, "--no-optional-locks", "blame", "--porcelain", "--contents", "-", "--", name)
	cmd.Stdin = strings.NewReader(string(text))
	out, err := cmd.Output()
	if err != nil {
		return Blame{}, gitError("blame", err)
	}
	b := parseBlame(strings.NewReader(string(out)))
	if you, err := cmdrun.Git(ctx, dir, "config", "user.email").Output(); err == nil {
		b.You = strings.TrimSpace(string(you))
	}
	return b, nil
}

// zeroSHA is the commit git blame names for lines not committed yet.
const zeroSHA = "0000000000000000000000000000000000000000"

// parseBlame reads git blame --porcelain: per line a header "<sha>
// <orig> <final> [<count>]", the commit's details the first time it
// appears, and the line itself after a tab.
func parseBlame(r io.Reader) Blame {
	b := Blame{Lines: []string{}, Commits: map[string]BlameCommit{}}
	sc := bufio.NewScanner(r)
	sc.Buffer(make([]byte, 64<<10), 16<<20)
	sha := ""
	var c BlameCommit
	for sc.Scan() {
		line := sc.Text()
		if strings.HasPrefix(line, "\t") {
			if sha != "" && sha != zeroSHA {
				if _, ok := b.Commits[sha]; !ok {
					b.Commits[sha] = c
				}
				b.Lines = append(b.Lines, sha)
			} else {
				b.Lines = append(b.Lines, "")
			}
			sha, c = "", BlameCommit{}
			continue
		}
		if sha == "" {
			if f := strings.Fields(line); len(f) >= 3 && len(f[0]) >= 40 && isSHA(f[0]) {
				sha = f[0]
				if old, ok := b.Commits[sha]; ok {
					c = old
				}
			}
			continue
		}
		key, val, _ := strings.Cut(line, " ")
		switch key {
		case "author":
			c.Author = val
		case "author-mail":
			c.Mail = strings.Trim(val, "<>")
		case "author-time":
			c.Time, _ = strconv.ParseInt(val, 10, 64)
		case "summary":
			c.Summary = val
		}
	}
	return b
}
