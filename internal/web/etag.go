package web

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"io/fs"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/ashish10alex/xf/internal/webtheme"
)

// The page's files carry an ETag from their content, so a reload, which
// revalidates each one (Cache-Control: no-cache), is answered 304 Not
// Modified instead of sending every module again. The files built into xf
// don't change while it runs; those XF_WEB_ASSETS serves have their
// modification time instead, which http.FileServer sends as Last-Modified.

func etagOf(b []byte) string {
	sum := sha256.Sum256(b)
	return `"` + hex.EncodeToString(sum[:12]) + `"`
}

// assetTags is the ETag of each file built into xf, by its URL path
// without the leading "/"; "" is index.html, which "/" serves.
var assetTags = sync.OnceValue(func() map[string]string {
	static, err := fs.Sub(assets, "assets")
	if err != nil {
		return nil
	}
	tags := map[string]string{}
	_ = fs.WalkDir(static, ".", func(name string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return err
		}
		b, err := fs.ReadFile(static, name)
		if err != nil {
			return err
		}
		tags[name] = etagOf(b)
		return nil
	})
	if tag, ok := tags["index.html"]; ok {
		tags[""] = tag
	}
	return tags
})

// assetTag sets the ETag of the built-in file r asks for, if it is one.
func assetTag(w http.ResponseWriter, r *http.Request, tags map[string]string) {
	if tag, ok := tags[strings.TrimPrefix(r.URL.Path, "/")]; ok {
		w.Header().Set("ETag", tag)
	}
}

var themesTag = sync.OnceValue(func() string { return etagOf([]byte(webtheme.CSS())) })

// themesCSS is themes.css: every theme's colours (see webtheme.CSS).
func themesCSS(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "text/css; charset=utf-8")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("ETag", themesTag())
	http.ServeContent(w, r, "themes.css", time.Time{}, bytes.NewReader([]byte(webtheme.CSS())))
}
