package forge

import (
	"bufio"
	"bytes"
	"context"
	"errors"
	"net/url"
	"os/exec"
	"strings"
	"sync"

	"github.com/ashish10alex/xf/internal/backend/cmdrun"
)

// PushRemote is the remote branch pushes to, as git push picks it:
// branch.<name>.pushRemote, remote.pushDefault, branch.<name>.remote, else
// origin.
func PushRemote(ctx context.Context, dir, branch string) string {
	for _, key := range []string{"branch." + branch + ".pushRemote", "remote.pushDefault", "branch." + branch + ".remote"} {
		if v := gitConfig(ctx, dir, key); v != "" && v != "." {
			return v
		}
	}
	return "origin"
}

func gitConfig(ctx context.Context, dir, key string) string {
	out, err := cmdrun.Git(ctx, dir, "config", "--get", key).Output()
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(out))
}

// RemoteURL is the URL remote pushes to.
func RemoteURL(ctx context.Context, dir, remote string) (string, error) {
	out, err := cmdrun.Git(ctx, dir, "remote", "get-url", "--push", remote).Output()
	if err != nil {
		if ctx.Err() != nil {
			return "", ctx.Err()
		}
		return "", errors.New("no remote " + remote)
	}
	return strings.TrimSpace(string(out)), nil
}

// Remote is a remote's URL in parts: its host, the project's path on it,
// and whether it goes over SSH, where the host may be an alias of
// ~/.ssh/config.
type Remote struct {
	Host, Path string
	SSH        bool
}

// ParseRemote reads a remote's URL: git@host:group/project.git,
// ssh://git@host:22/group/project.git or https://host/group/project.git.
// An https URL keeps its port, which its API is on too; an SSH one drops
// it.
func ParseRemote(raw string) (Remote, bool) {
	raw = strings.TrimSpace(raw)
	var r Remote
	if strings.Contains(raw, "://") {
		u, err := url.Parse(raw)
		if err != nil || u.Host == "" {
			return Remote{}, false
		}
		switch u.Scheme {
		case "ssh", "git+ssh", "ssh+git":
			r.SSH, r.Host = true, u.Hostname()
		case "https", "http":
			r.Host = u.Host
		default:
			return Remote{}, false
		}
		r.Path = u.Path
	} else {
		// scp-like: [user@]host:path
		host, path, ok := strings.Cut(raw, ":")
		if !ok || strings.Contains(host, "/") {
			return Remote{}, false
		}
		if _, h, ok := strings.Cut(host, "@"); ok {
			host = h
		}
		r.SSH, r.Host, r.Path = true, host, path
	}
	r.Host = strings.ToLower(r.Host)
	r.Path = strings.TrimSuffix(strings.Trim(r.Path, "/"), ".git")
	r.Path = strings.Trim(r.Path, "/")
	if r.Host == "" || r.Path == "" {
		return Remote{}, false
	}
	return r, true
}

// sshHosts are the real hosts of the SSH aliases asked about so far.
var sshHosts sync.Map

// SSHHost is the host an SSH alias stands for, as `ssh -G` resolves it
// from ~/.ssh/config; the alias itself when ssh says nothing else or
// isn't there.
func SSHHost(ctx context.Context, alias string) string {
	if v, ok := sshHosts.Load(alias); ok {
		return v.(string)
	}
	host := alias
	if out, err := exec.CommandContext(ctx, "ssh", "-G", alias).Output(); err == nil {
		sc := bufio.NewScanner(bytes.NewReader(out))
		for sc.Scan() {
			if k, v, ok := strings.Cut(sc.Text(), " "); ok && k == "hostname" && v != "" {
				host = strings.ToLower(strings.TrimSpace(v))
				break
			}
		}
		sshHosts.Store(alias, host)
	}
	return host
}

// Match is the host xf knows that a remote is on, trying an SSH alias's
// real host first, then the alias as an entry may name it.
func Match(ctx context.Context, s *Store, r Remote) (Access, bool) {
	if r.SSH {
		if h := SSHHost(ctx, r.Host); h != r.Host {
			if a, ok := s.Lookup(h); ok {
				return a, true
			}
		}
	}
	return s.Lookup(r.Host)
}
