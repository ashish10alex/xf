package dataform

import (
	"bytes"
	"context"
	"fmt"
	"regexp"
	"strings"
	"time"

	"github.com/ashish10alex/xf/internal/backend/cmdrun"
	"github.com/ashish10alex/xf/internal/project"
)

// Compile runs `dataform compile --json` in root and parses the result.
// Dataform exits non-zero when the project has compilation errors but still
// prints the compile result, so stdout is parsed whatever the exit code; the
// errors end up in Graph.Errors. An error is returned only when there is no
// usable compile result.
func Compile(ctx context.Context, bin, root string) (*project.Graph, error) {
	return CompileWith(ctx, bin, root, nil)
}

// CompileWith is Compile with extra compiler options, such as
// --schema-suffix=dev or --vars=env=dev.
//
// A compile that fails because a file under .git went away while Dataform
// copied the project is run again; see gitRace.
func CompileWith(ctx context.Context, bin, root string, options []string) (*project.Graph, error) {
	bin, err := which(bin) // named in errors
	if err != nil {
		return nil, err
	}
	prog, prefix, err := command(bin)
	if err != nil {
		return nil, err
	}
	for attempt := 1; ; attempt++ {
		g, detail, err := compileOnce(ctx, bin, prog, prefix, root, options)
		if err == nil || ctx.Err() != nil || !gitRace.MatchString(detail) {
			return g, err
		}
		if attempt == raceAttempts {
			return nil, fmt.Errorf("%w\n(a git command changed .git while dataform copied the project; compile again)", err)
		}
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-time.After(time.Duration(attempt) * 200 * time.Millisecond):
		}
	}
}

// gitRace matches the error of a Dataform compile that copied the project
// (it does when workflow_settings.yaml sets dataformCoreVersion) while a
// git command ran in it: the copy lists .git, git removes a file it listed
// (index.lock, most often), and the copy stops on it. A git status or diff
// that refreshes the index does this, such as an editor's on save.
var gitRace = regexp.MustCompile(`ENOENT[^\n]*[/\\]\.git[/\\]`)

// raceAttempts is how many times CompileWith runs a compile that gitRace
// stops.
const raceAttempts = 4

// compileOnce runs one compile. detail is what dataform printed when there
// is no compile result.
func compileOnce(ctx context.Context, bin, prog string, prefix []string, root string, options []string) (*project.Graph, string, error) {
	cmd := cmdrun.Command(ctx, prog, append(append(prefix, "compile", "--json"), options...)...)
	cmd.Dir = root
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	runErr := cmd.Run()
	if ctx.Err() != nil {
		return nil, "", ctx.Err()
	}

	if data, err := ExtractCompileJSON(stdout.Bytes()); err == nil {
		g, err := ParseCompileJSON(data)
		return g, "", err
	}

	detail := strings.TrimSpace(stripANSI(stderr.String()))
	if detail == "" {
		detail = strings.TrimSpace(stdout.String())
	}
	if detail == "" {
		detail = "(no output)"
	}
	if runErr != nil {
		return nil, detail, fmt.Errorf("%s compile --json: %w\n%s", bin, runErr, detail)
	}
	return nil, detail, fmt.Errorf("%s compile --json printed no compile result:\n%s", bin, detail)
}

// SplitOptions splits compiler options written as on a command line into
// arguments: on spaces, outside single or double quotes, which are removed.
func SplitOptions(s string) ([]string, error) {
	var args []string
	var cur strings.Builder
	var quote rune
	inArg := false
	for _, r := range s {
		switch {
		case quote != 0 && r == quote:
			quote = 0
		case quote != 0:
			cur.WriteRune(r)
		case r == '\'' || r == '"':
			quote, inArg = r, true
		case r == ' ' || r == '\t' || r == '\n':
			if inArg {
				args = append(args, cur.String())
				cur.Reset()
				inArg = false
			}
		default:
			cur.WriteRune(r)
			inArg = true
		}
	}
	if quote != 0 {
		return nil, fmt.Errorf("unclosed %c in compiler options %q", quote, s)
	}
	if inArg {
		args = append(args, cur.String())
	}
	return args, nil
}
