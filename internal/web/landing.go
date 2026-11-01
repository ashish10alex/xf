package web

import (
	"context"
	"encoding/json"
	"net/http"

	"github.com/ashish10alex/xf/internal/frontend"
	"github.com/ashish10alex/xf/internal/gcpauth"
)

// The picker's sign-in banner and the settings page's read-only rows
// outside any project. Both ask gcloud and Google, which takes a second
// or two, so the page asks for them after it shows.

// pickerInfo answers with the settings page's rows outside any project.
func (s *Server) pickerInfo(w http.ResponseWriter, r *http.Request) {
	writeRows(w, r, func(ctx context.Context, emit func([]frontend.InfoRow)) {
		if s.cfg.Landing == nil {
			emit(nil)
			return
		}
		s.cfg.Landing(ctx, emit)
	})
}

// writeRows answers with the info rows stream gives emit. With ?stream=1
// each is a line of JSON, sent as it comes: the first at once, with "…"
// for each lookup still going, and one more as each lands, so a slow
// gcloud doesn't hold up the rest. Without it, one answer, the last.
func writeRows(w http.ResponseWriter, r *http.Request, stream func(ctx context.Context, emit func([]frontend.InfoRow))) {
	if r.URL.Query().Get("stream") != "1" {
		var last []frontend.InfoRow
		stream(r.Context(), func(rows []frontend.InfoRow) { last = rows })
		writeJSON(w, nonNil(last))
		return
	}
	w.Header().Set("Content-Type", "application/x-ndjson")
	w.Header().Set("Cache-Control", "no-store")
	flusher, _ := w.(http.Flusher)
	enc := json.NewEncoder(w) // each value on its own line
	stream(r.Context(), func(rows []frontend.InfoRow) {
		if r.Context().Err() != nil {
			return
		}
		_ = enc.Encode(nonNil(rows))
		if flusher != nil {
			flusher.Flush()
		}
	})
}

// pickerAuth answers with the sign-in problems the picker warns of.
func (s *Server) pickerAuth(w http.ResponseWriter, r *http.Request) {
	var alerts []gcpauth.Alert
	if s.cfg.Auth != nil {
		alerts = s.cfg.Auth(r.Context())
	}
	writeJSON(w, nonNil(alerts))
}
