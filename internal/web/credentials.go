package web

import "net/http"

// A new Google login (gcloud auth application-default login, say) is
// used from the next call: the clients follow the credential file
// (gcpauth.Credentials). The answers that failed before it may work now,
// so a project about to answer from its cached dry runs first looks for
// one, as does a page coming back into focus (from a terminal that just
// logged in), and on one every open project forgets its failed dry runs and
// table lookups, its pages ask for their details again, and every page
// says why.

// credsChangedText is what the pages say on a new login.
const credsChangedText = "Google login changed: retrying failed dry runs"

// checkCreds looks for a new login. Call it without p.mu held.
func (p *projectState) checkCreds() {
	if p.credsCheck != nil {
		p.credsCheck()
	}
}

// credsChecked looks for a new login (Config.Credentials), and acts on
// one.
func (s *Server) credsChecked() {
	if s.cfg.Credentials == nil {
		return
	}
	gen := s.cfg.Credentials()
	s.mu.Lock()
	if gen == s.credsGen {
		s.mu.Unlock()
		return
	}
	s.credsGen = gen
	open := make([]*projectState, 0, len(s.open))
	for _, op := range s.open {
		open = append(open, op.p)
	}
	s.mu.Unlock()
	for _, p := range open {
		p.credsChanged()
	}
	s.hub.send("credentials", map[string]string{"text": credsChangedText})
}

// credsChanged forgets the failed dry runs and table lookups, as a
// recompile does, and has the pages ask again. The engine forgets its
// own failed dry runs when it next dry-runs (engine.Config.Credentials).
func (p *projectState) credsChanged() {
	p.mu.Lock()
	p.forgetFailedDryRuns()
	p.expireTables(true)
	v := p.stateViewLocked()
	p.mu.Unlock()
	p.hub.send("state", v)
	p.hub.send("detail", nil)
}

// checkCredentials looks for a new login: a page asks as it comes back
// into focus.
func (s *Server) checkCredentials(w http.ResponseWriter, _ *http.Request) {
	s.credsChecked()
	w.WriteHeader(http.StatusNoContent)
}
