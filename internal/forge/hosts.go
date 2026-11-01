package forge

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
)

// Host is an entry of tokens.json: a host, what it runs and xf's token
// for it.
type Host struct {
	Name  string `json:"host"`
	Kind  Kind   `json:"kind"`
	Token string `json:"token,omitempty"`
}

// tokensFile is the file's shape.
type tokensFile struct {
	Hosts []Host `json:"hosts"`
}

// FileName is the tokens' file, in the user's config folder.
const FileName = "tokens.json"

// builtin are the hosts known without an entry.
var builtin = []Host{{Name: "github.com", Kind: GitHub}, {Name: "gitlab.com", Kind: GitLab}}

// envVar is the environment variable whose token a host of kind takes.
func envVar(k Kind) string {
	if k == GitLab {
		return "GITLAB_TOKEN"
	}
	return "GITHUB_TOKEN"
}

// Store is tokens.json, at Path, written with mode 0600 as it holds
// secrets. Getenv reads the environment, whose GITLAB_TOKEN and
// GITHUB_TOKEN win for gitlab.com and github.com and stand in for a
// self-hosted host of their kind that has no token in the file.
type Store struct {
	Path   string
	Getenv func(string) string
}

// NewStore is the store in the config folder dir.
func NewStore(dir string) *Store {
	return &Store{Path: filepath.Join(dir, FileName), Getenv: os.Getenv}
}

// Load reads the entries; no file is none.
func (s *Store) Load() ([]Host, error) {
	b, err := os.ReadFile(s.Path)
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var f tokensFile
	if err := json.Unmarshal(b, &f); err != nil {
		return nil, fmt.Errorf("%s: %w", s.Path, err)
	}
	return f.Hosts, nil
}

func (s *Store) save(hosts []Host) error {
	if hosts == nil {
		hosts = []Host{}
	}
	b, err := json.MarshalIndent(tokensFile{Hosts: hosts}, "", "  ")
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(s.Path), 0o755); err != nil {
		return err
	}
	tmp, err := os.CreateTemp(filepath.Dir(s.Path), ".tokens-*.json")
	if err != nil {
		return err
	}
	defer os.Remove(tmp.Name())
	if err := tmp.Chmod(0o600); err != nil && !errors.Is(err, errors.ErrUnsupported) {
		tmp.Close()
		return err
	}
	if _, err := tmp.Write(append(b, '\n')); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	return os.Rename(tmp.Name(), s.Path)
}

// CleanName is a host's name as entries keep it: lower case, without a
// path or https://. http:// stays, for a host that only speaks it (a test
// server).
func CleanName(name string) string {
	name = strings.ToLower(strings.TrimSpace(name))
	scheme := ""
	if s, rest, ok := strings.Cut(name, "://"); ok {
		scheme, name = s, rest
	}
	name, _, _ = strings.Cut(name, "/")
	if scheme == "http" && name != "" {
		return "http://" + name
	}
	return name
}

// bareName is a host's name without http://, as a remote's URL has it.
func bareName(name string) string { return strings.TrimPrefix(name, "http://") }

// Set adds an entry or changes one. An empty token keeps the token the
// entry had, unless clear is set.
func (s *Store) Set(h Host, clear bool) error {
	h.Name = CleanName(h.Name)
	if h.Name == "" {
		return errors.New("no host")
	}
	if !h.Kind.Valid() {
		return fmt.Errorf("a host runs gitlab or github, not %q", h.Kind)
	}
	h.Token = strings.TrimSpace(h.Token)
	hosts, err := s.Load()
	if err != nil {
		return err
	}
	if i := slices.IndexFunc(hosts, func(x Host) bool { return x.Name == h.Name }); i >= 0 {
		if h.Token == "" && !clear {
			h.Token = hosts[i].Token
		}
		hosts[i] = h
	} else {
		hosts = append(hosts, h)
	}
	return s.save(hosts)
}

// Delete takes an entry out.
func (s *Store) Delete(name string) error {
	name = CleanName(name)
	hosts, err := s.Load()
	if err != nil {
		return err
	}
	out := slices.DeleteFunc(hosts, func(x Host) bool { return x.Name == name })
	return s.save(out)
}

// Access is what xf has for a host: its kind, its token and where the
// token comes from ("file", or an environment variable's name).
type Access struct {
	Name  string
	Kind  Kind
	Token string
	From  string
	// Builtin says the host is known without an entry.
	Builtin bool
	// Entry says tokens.json has an entry for it.
	Entry bool
}

// Hosts are every host xf knows: the entries, then the built-in ones
// with none.
func (s *Store) Hosts() ([]Access, error) {
	hosts, err := s.Load()
	var out []Access
	seen := map[string]bool{}
	for _, h := range hosts {
		if h.Name == "" || seen[h.Name] {
			continue
		}
		seen[h.Name] = true
		out = append(out, s.access(h, true))
	}
	for _, h := range builtin {
		if !seen[h.Name] {
			out = append(out, s.access(h, false))
		}
	}
	return out, err
}

func (s *Store) access(h Host, entry bool) Access {
	a := Access{Name: h.Name, Kind: h.Kind, Token: h.Token, Entry: entry,
		Builtin: slices.ContainsFunc(builtin, func(b Host) bool { return b.Name == h.Name })}
	if a.Token != "" {
		a.From = "file"
	}
	if s.Getenv == nil || !a.Kind.Valid() {
		return a
	}
	v := envVar(a.Kind)
	if env := strings.TrimSpace(s.Getenv(v)); env != "" && (a.Builtin || a.Token == "") {
		a.Token, a.From = env, v
	}
	return a
}

// Lookup is what xf has for the host named name, if it knows it.
func (s *Store) Lookup(name string) (Access, bool) {
	name = bareName(CleanName(name))
	all, _ := s.Hosts()
	for _, a := range all {
		if bareName(a.Name) == name {
			return a, true
		}
	}
	return Access{}, false
}

// Mask is a token as the settings page shows it: its last four
// characters.
func Mask(token string) string {
	if token == "" {
		return ""
	}
	if len(token) <= 8 {
		return "••••"
	}
	return "••••" + token[len(token)-4:]
}
