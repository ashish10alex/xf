package web

import (
	"bytes"
	"net/http"
	"net/url"
	"os"
	"path"
	"strconv"
	"strings"
	"unicode"

	"github.com/alecthomas/chroma/v2"
	"github.com/alecthomas/chroma/v2/lexers"
	"github.com/yuin/goldmark"
	"github.com/yuin/goldmark/ast"
	"github.com/yuin/goldmark/extension"
	"github.com/yuin/goldmark/parser"
	"github.com/yuin/goldmark/renderer"
	"github.com/yuin/goldmark/text"
	"github.com/yuin/goldmark/util"
)

// A Markdown file in the source pane shows rendered in pane 2 (ADR 0027).
// The page sends the editor's text, unsaved changes and all, and draws
// the HTML that comes back:
//
//   - goldmark with GitHub's extensions (tables, task lists, strikethrough,
//     bare links); raw HTML in the file is left out, and so are
//     javascript: links
//   - code blocks coloured by chroma, with the SQL pane's classes
//   - headings with ids, "md-" and GitHub's slug, so "#a-heading" links work
//     and never clash with the page's own ids
//   - a link to a file of the project carries data-file, its path from
//     the project's root, which the page opens in the source pane
//   - an image of the project loads from /api/markdown/image

// markdown is the renderer, the same for every file: what it needs of a
// file comes in the parser's context.
var markdown = goldmark.New(
	goldmark.WithExtensions(extension.GFM),
	goldmark.WithParserOptions(
		parser.WithAutoHeadingID(),
		parser.WithASTTransformers(util.Prioritized(markdownLinks{}, 100)),
	),
	goldmark.WithRendererOptions(
		renderer.WithNodeRenderers(util.Prioritized(markdownCode{}, 100)),
	),
)

var (
	mdFileKey    = parser.NewContextKey() // the file's path from the project's root
	mdProjectKey = parser.NewContextKey() // the project, for its images' URLs
	mdIsFileKey  = parser.NewContextKey() // says whether a path is a file of the project
)

// renderMarkdown is src, the text of file, as HTML. project names the
// project the images load from, as the page names it; isFile says which
// links go to a file of it.
func renderMarkdown(file, src, project string, isFile func(string) bool) (string, error) {
	ctx := parser.NewContext(parser.WithIDs(&headingIDs{used: map[string]bool{}}))
	ctx.Set(mdFileKey, file)
	ctx.Set(mdProjectKey, project)
	ctx.Set(mdIsFileKey, isFile)
	var b bytes.Buffer
	if err := markdown.Convert([]byte(src), &b, parser.WithContext(ctx)); err != nil {
		return "", err
	}
	return b.String(), nil
}

// markdownPreview answers {file, text} with {html}: text, file's as the
// editor has it, rendered.
func (p *projectState) markdownPreview(w http.ResponseWriter, r *http.Request) {
	var req struct {
		File string `json:"file"`
		Text string `json:"text"`
	}
	if !readSourceJSON(w, r, &req) {
		return
	}
	if _, err := p.projectPath(req.File); err != nil {
		sourceError(w, err)
		return
	}
	isFile := func(f string) bool { _, err := p.sourcePath(f); return err == nil }
	out, err := renderMarkdown(req.File, req.Text, requestKey(r), isFile)
	if err != nil {
		writeError(w, err)
		return
	}
	writeJSON(w, struct {
		HTML string `json:"html"`
	}{out})
}

// markdownImageTypes are the files an image of the preview may be, by
// extension.
var markdownImageTypes = map[string]string{
	".png": "image/png", ".jpg": "image/jpeg", ".jpeg": "image/jpeg", ".gif": "image/gif",
	".webp": "image/webp", ".avif": "image/avif", ".bmp": "image/bmp", ".ico": "image/x-icon",
	".svg": "image/svg+xml",
}

// markdownImage answers ?file, an image of the project. An <img> can't
// send the project's header, so ?project names it (see projectRoutes).
// The image is sandboxed, so an SVG opened on its own runs no script.
func (p *projectState) markdownImage(w http.ResponseWriter, r *http.Request) {
	file := r.URL.Query().Get("file")
	ctype, ok := markdownImageTypes[strings.ToLower(path.Ext(file))]
	if !ok {
		http.Error(w, file+" isn't an image", http.StatusForbidden)
		return
	}
	full, err := p.sourcePath(file)
	if err != nil {
		sourceError(w, err)
		return
	}
	f, err := os.Open(full)
	if err != nil {
		writeError(w, err)
		return
	}
	defer f.Close()
	fi, err := f.Stat()
	if err != nil {
		writeError(w, err)
		return
	}
	h := w.Header()
	h.Set("Content-Type", ctype)
	h.Set("Content-Security-Policy", "default-src 'none'; style-src 'unsafe-inline'; sandbox")
	h.Set("X-Content-Type-Options", "nosniff")
	h.Set("Cache-Control", "no-cache")
	http.ServeContent(w, r, "", fi.ModTime(), f)
}

// markdownLinks points the file's links and images at the project's
// files.
type markdownLinks struct{}

func (markdownLinks) Transform(doc *ast.Document, _ text.Reader, pc parser.Context) {
	file, _ := pc.Get(mdFileKey).(string)
	project, _ := pc.Get(mdProjectKey).(string)
	isFile, _ := pc.Get(mdIsFileKey).(func(string) bool)
	dir := path.Dir(file)
	_ = ast.Walk(doc, func(n ast.Node, entering bool) (ast.WalkStatus, error) {
		if !entering {
			return ast.WalkContinue, nil
		}
		switch n := n.(type) {
		case *ast.Link:
			if to, ok := projectLink(dir, string(n.Destination)); ok && isFile != nil && isFile(to) {
				n.SetAttributeString("data-file", []byte(to))
			}
		case *ast.Image:
			if to, ok := projectLink(dir, string(n.Destination)); ok {
				q := url.Values{"file": {to}}
				if project != "" {
					q.Set("project", project)
				}
				n.Destination = []byte("/api/markdown/image?" + q.Encode())
			}
		}
		return ast.WalkContinue, nil
	})
}

// projectLink is the file a link in a file of dir goes to, from the
// project's root: a relative link is from dir, one that starts with "/"
// from the root, as GitHub has them. A link with a scheme or a host, or
// to a place in the file itself, or out of the project, is no file's.
func projectLink(dir, dest string) (string, bool) {
	u, err := url.Parse(dest)
	if err != nil || u.Scheme != "" || u.Host != "" || u.Path == "" {
		return "", false
	}
	var to string
	if strings.HasPrefix(u.Path, "/") {
		to = path.Clean(strings.TrimLeft(u.Path, "/"))
	} else {
		to = path.Clean(path.Join(dir, u.Path))
	}
	if to == "." || to == ".." || strings.HasPrefix(to, "../") {
		return "", false
	}
	return to, true
}

// headingIDs gives headings GitHub's ids, "md-" in front.
type headingIDs struct{ used map[string]bool }

func (ids *headingIDs) Generate(value []byte, _ ast.NodeKind) []byte {
	base := "md-" + slug(string(value))
	id := base
	for i := 1; ids.used[id]; i++ {
		id = base + "-" + strconv.Itoa(i)
	}
	ids.used[id] = true
	return []byte(id)
}

func (ids *headingIDs) Put(value []byte) { ids.used[string(value)] = true }

// slug is a heading's id as GitHub makes it: lower case, a space a "-",
// letters, digits, "-" and "_" kept and anything else left out.
func slug(s string) string {
	var b strings.Builder
	for _, r := range strings.ToLower(strings.TrimSpace(s)) {
		switch {
		case r == ' ':
			b.WriteByte('-')
		case r == '-' || r == '_' || unicode.IsLetter(r) || unicode.IsDigit(r):
			b.WriteRune(r)
		}
	}
	return b.String()
}

// markdownCode draws code blocks as the SQL pane draws SQL: chroma's
// tokens, each in a span of its class.
type markdownCode struct{}

func (markdownCode) RegisterFuncs(reg renderer.NodeRendererFuncRegisterer) {
	reg.Register(ast.KindFencedCodeBlock, renderCodeBlock)
	reg.Register(ast.KindCodeBlock, renderCodeBlock)
}

func renderCodeBlock(w util.BufWriter, src []byte, n ast.Node, entering bool) (ast.WalkStatus, error) {
	if !entering {
		return ast.WalkContinue, nil
	}
	var code bytes.Buffer
	lines := n.Lines()
	for i := 0; i < lines.Len(); i++ {
		seg := lines.At(i)
		code.Write(seg.Value(src))
	}
	lang := ""
	if f, ok := n.(*ast.FencedCodeBlock); ok {
		lang = string(f.Language(src))
	}
	_, _ = w.WriteString(`<pre class="md-code"><code>`)
	writeTokens(w, codeLexer(lang), code.String())
	_, _ = w.WriteString("</code></pre>\n")
	return ast.WalkSkipChildren, nil
}

// codeLexer is chroma's lexer for a code block's language, nil for one
// it doesn't know.
func codeLexer(lang string) chroma.Lexer {
	switch strings.ToLower(lang) {
	case "":
		return nil
	case "sqlx":
		lang = "sql"
	}
	return lexers.Get(lang)
}

func writeTokens(w util.BufWriter, lexer chroma.Lexer, code string) {
	if lexer != nil {
		if it, err := chroma.Coalesce(lexer).Tokenise(nil, code); err == nil {
			for tok := it(); tok != chroma.EOF; tok = it() {
				class := tokenClass(tok.Type)
				if class == "" {
					_, _ = w.Write(util.EscapeHTML([]byte(tok.Value)))
					continue
				}
				_, _ = w.WriteString(`<span class="t-` + class + `">`)
				_, _ = w.Write(util.EscapeHTML([]byte(tok.Value)))
				_, _ = w.WriteString("</span>")
			}
			return
		}
	}
	_, _ = w.Write(util.EscapeHTML([]byte(code)))
}
