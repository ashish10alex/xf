package main

import (
	"archive/tar"
	"archive/zip"
	"bytes"
	"compress/gzip"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"flag"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path"
	"path/filepath"
	"runtime"
	"runtime/debug"
	"strings"
	"time"
)

// releasesURL is where xf's GitHub Releases are; tests point it at a fake.
var releasesURL = "https://github.com/ashish10alex/xf/releases"

// installMethod is how an xf executable was installed, which says how
// xf update updates it.
type installMethod int

const (
	// installRelease is a release archive, from the install script or by
	// hand: xf update replaces the executable with the latest release's.
	installRelease installMethod = iota
	installHomebrew
	installScoop
	installWinget
	installGo     // go install …@version
	installSource // built from a checkout
)

// detectInstall says how the xf at exe was installed, from its path and
// the build info Go recorded in it (nil when there is none).
func detectInstall(exe string, bi *debug.BuildInfo) installMethod {
	p := strings.ToLower(strings.ReplaceAll(exe, `\`, "/"))
	switch {
	case strings.Contains(p, "/caskroom/xf/") || strings.Contains(p, "/cellar/xf/"):
		return installHomebrew
	case strings.Contains(p, "/scoop/apps/xf/"):
		return installScoop
	case strings.Contains(p, "/winget/packages/ashishalex.xf"):
		return installWinget
	}
	if bi == nil || releaseVersion(bi) == "" {
		return installSource
	}
	// go install …@version builds from the module download, whose checksum
	// it records; a release is built from a checkout, which has none.
	if bi.Main.Sum != "" {
		return installGo
	}
	return installRelease
}

// releaseVersion is the release tag xf was built from (v0.0.2), or "" for
// a build from an untagged commit or one with no version.
func releaseVersion(bi *debug.BuildInfo) string {
	if bi == nil {
		return ""
	}
	v := bi.Main.Version
	if v == "" || v == "(devel)" || pseudoVersion.MatchString(v) || strings.Contains(v, "+dirty") {
		return ""
	}
	return v
}

func runUpdate(args []string) error {
	fs := flag.NewFlagSet("xf update", flag.ContinueOnError)
	fs.Usage = func() {
		_, _ = io.WriteString(fs.Output(), `usage: xf update (or xf --update)

Updates xf to the latest release, the way it was installed: with brew or
go install when it came from them (scoop and winget print the command to
run), otherwise by replacing this executable with the release's, checked
against the release's checksums.txt.
`)
	}
	if help, err := parseArgs(fs, args); help || err != nil {
		return err
	}
	if fs.NArg() > 0 {
		return fmt.Errorf("unexpected arguments: %v (see xf update --help)", fs.Args())
	}
	exe, err := os.Executable()
	if err != nil {
		return fmt.Errorf("can't find this xf's executable: %w", err)
	}
	if resolved, err := filepath.EvalSymlinks(exe); err == nil {
		exe = resolved
	}
	bi, _ := debug.ReadBuildInfo()
	return update(os.Stdout, exe, bi, detectInstall(exe, bi))
}

// update updates the xf at exe, installed by method, writing what it does
// to w.
func update(w io.Writer, exe string, bi *debug.BuildInfo, method installMethod) error {
	// What to run to update xf with the package manager that installed it.
	var cmd []string
	switch method {
	case installSource:
		return errors.New("this xf was built from source, not installed from a release (see xf --version); to update it, pull the checkout and run `just install` (or `go install ./cmd/xf`)")
	case installHomebrew:
		cmd = []string{"brew", "upgrade", "--cask", "ashish10alex/tap/xf"}
	case installGo:
		cmd = []string{"go", "install", "github.com/ashish10alex/xf/cmd/xf@latest"}
	case installScoop:
		cmd = []string{"scoop", "update", "xf"}
	case installWinget:
		cmd = []string{"winget", "upgrade", "--id", "AshishAlex.xf"}
	case installRelease:
		return selfUpdate(w, http.DefaultClient, exe, releaseVersion(bi), runtime.GOOS, runtime.GOARCH)
	}
	// Scoop and winget can't replace an xf.exe that runs, and this one does.
	if method == installScoop || method == installWinget {
		_, _ = fmt.Fprintf(w, "xf was installed with %s, which can't replace xf.exe while this xf runs it. To update, run:\n\n  xf stop\n  %s\n", cmd[0], strings.Join(cmd, " "))
		return errQuiet
	}
	if _, err := exec.LookPath(cmd[0]); err != nil {
		return fmt.Errorf("xf was installed with %s, which isn't on your PATH; to update, run: %s", cmd[0], strings.Join(cmd, " "))
	}
	_, _ = fmt.Fprintln(w, "updating with:", strings.Join(cmd, " "))
	restore := func() {}
	if runtime.GOOS == "windows" {
		// go install can't write over the xf.exe this xf runs from; it can
		// be moved aside, as install.ps1 does.
		old, err := moveAside(exe)
		if err != nil {
			return err
		}
		restore = func() {
			if _, err := os.Stat(exe); os.IsNotExist(err) {
				_ = os.Rename(old, exe)
			}
		}
	}
	c := exec.Command(cmd[0], cmd[1:]...)
	c.Stdin, c.Stdout, c.Stderr = os.Stdin, w, os.Stderr
	if err := c.Run(); err != nil {
		restore()
		return fmt.Errorf("%s: %w", strings.Join(cmd, " "), err)
	}
	restore()
	return nil
}

// selfUpdate replaces the xf at exe, release current ("" when unknown),
// with the latest release's build for goos and goarch, if it is newer.
func selfUpdate(w io.Writer, client *http.Client, exe, current, goos, goarch string) error {
	latest, err := latestRelease(client)
	if err != nil {
		return err
	}
	if latest == current {
		_, _ = fmt.Fprintf(w, "xf %s is the latest release\n", current)
		return nil
	}
	archive := "xf_" + goos + "_" + goarch + ".tar.gz"
	if goos == "windows" {
		archive = "xf_" + goos + "_" + goarch + ".zip"
	}
	base := releasesURL + "/download/" + latest + "/"
	_, _ = fmt.Fprintf(w, "downloading xf %s (%s)\n", latest, archive)
	data, err := download(client, base+archive)
	if err != nil {
		return err
	}
	sums, err := download(client, base+"checksums.txt")
	if err != nil {
		return err
	}
	if err := checkSum(data, sums, archive); err != nil {
		return err
	}
	bin, err := extractXF(data, archive)
	if err != nil {
		return fmt.Errorf("%s: %w; nothing was installed", archive, err)
	}
	if err := replaceExe(exe, bin); err != nil {
		return err
	}
	from := current
	if from == "" {
		from = "an unknown version"
	}
	_, _ = fmt.Fprintf(w, "updated %s from %s to %s\n", exe, from, latest)
	return nil
}

// latestRelease is the tag of xf's latest release, read from where
// releases/latest redirects (…/releases/tag/v0.0.3); unlike GitHub's API,
// that isn't rate limited.
func latestRelease(client *http.Client) (string, error) {
	c := *client
	c.Timeout = 30 * time.Second
	c.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
	resp, err := c.Get(releasesURL + "/latest")
	if err != nil {
		return "", fmt.Errorf("can't find xf's latest release: %w", err)
	}
	_ = resp.Body.Close()
	loc := resp.Header.Get("Location")
	tag := path.Base(loc)
	if resp.StatusCode/100 != 3 || !strings.Contains(loc, "/releases/tag/") || !strings.HasPrefix(tag, "v") {
		return "", fmt.Errorf("can't find xf's latest release: %s answered %s, not a redirect to a release (a proxy may be in the way)", releasesURL+"/latest", resp.Status)
	}
	return tag, nil
}

// maxDownload bounds what download reads: an archive is about 15 MB.
const maxDownload = 200 << 20

// download reads url's body. A proxy that blocks or holds the file answers
// with its own web page, which is said so.
func download(client *http.Client, url string) ([]byte, error) {
	c := *client
	c.Timeout = 5 * time.Minute
	resp, err := c.Get(url)
	if err != nil {
		return nil, fmt.Errorf("downloading %s: %w; nothing was installed", url, err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("downloading %s: %s; nothing was installed", url, resp.Status)
	}
	data, err := io.ReadAll(io.LimitReader(resp.Body, maxDownload))
	if err != nil {
		return nil, fmt.Errorf("downloading %s: %w; nothing was installed", url, err)
	}
	if strings.Contains(resp.Header.Get("Content-Type"), "html") || bytes.HasPrefix(bytes.TrimSpace(data), []byte("<")) {
		return nil, fmt.Errorf("downloading %s returned a web page instead of the file, so nothing was installed; a proxy or firewall is probably scanning or blocking it: wait a few minutes and run xf update again", url)
	}
	return data, nil
}

// checkSum checks data against the SHA-256 checksums.txt (sums) lists for
// the file name.
func checkSum(data, sums []byte, name string) error {
	got := sha256.Sum256(data)
	for line := range strings.Lines(string(sums)) {
		f := strings.Fields(line)
		if len(f) == 2 && f[1] == name {
			if f[0] == hex.EncodeToString(got[:]) {
				return nil
			}
			break
		}
	}
	return fmt.Errorf("%s doesn't match checksums.txt; nothing was installed", name)
}

// extractXF reads the xf executable out of a release archive, a zip on
// Windows and a .tar.gz elsewhere.
func extractXF(data []byte, archive string) ([]byte, error) {
	if strings.HasSuffix(archive, ".zip") {
		zr, err := zip.NewReader(bytes.NewReader(data), int64(len(data)))
		if err != nil {
			return nil, err
		}
		for _, f := range zr.File {
			if f.Name == "xf.exe" {
				rc, err := f.Open()
				if err != nil {
					return nil, err
				}
				defer func() { _ = rc.Close() }()
				return io.ReadAll(io.LimitReader(rc, maxDownload))
			}
		}
		return nil, errors.New("no xf.exe in it")
	}
	gz, err := gzip.NewReader(bytes.NewReader(data))
	if err != nil {
		return nil, err
	}
	tr := tar.NewReader(gz)
	for {
		h, err := tr.Next()
		if err == io.EOF {
			return nil, errors.New("no xf in it")
		}
		if err != nil {
			return nil, err
		}
		if h.Name == "xf" && h.Typeflag == tar.TypeReg {
			return io.ReadAll(io.LimitReader(tr, maxDownload))
		}
	}
}

// replaceExe puts bin in exe's place. It is written beside exe and renamed
// over it, so exe is never half written, a running xf keeps its file, and
// macOS doesn't kill the new one for a stale code signature. Windows can't
// rename over an xf.exe that runs, so the old one is moved aside first.
func replaceExe(exe string, bin []byte) error {
	dir := filepath.Dir(exe)
	tmp, err := os.CreateTemp(dir, ".xf-update-*")
	if err != nil {
		return fmt.Errorf("can't write to %s, where xf is: %w; nothing was installed", dir, err)
	}
	_, err = tmp.Write(bin)
	if cerr := tmp.Close(); err == nil {
		err = cerr
	}
	if err == nil {
		err = os.Chmod(tmp.Name(), 0o755)
	}
	if err != nil {
		_ = os.Remove(tmp.Name())
		return fmt.Errorf("writing the new xf beside %s: %w; nothing was installed", exe, err)
	}
	if runtime.GOOS != "windows" {
		if err := os.Rename(tmp.Name(), exe); err != nil {
			_ = os.Remove(tmp.Name())
			return fmt.Errorf("replacing %s: %w; nothing was installed", exe, err)
		}
		return nil
	}
	old, err := moveAside(exe)
	if err != nil {
		_ = os.Remove(tmp.Name())
		return err
	}
	if err := os.Rename(tmp.Name(), exe); err != nil {
		_ = os.Rename(old, exe)
		_ = os.Remove(tmp.Name())
		return fmt.Errorf("replacing %s: %w; nothing was installed", exe, err)
	}
	removeOld(exe)
	return nil
}

// moveAside renames exe to exe.old-<time>, which Windows allows while it
// runs, after removing the older ones nothing runs any more, and returns
// the new name.
func moveAside(exe string) (string, error) {
	removeOld(exe)
	old := exe + ".old-" + time.Now().Format("20060102150405")
	if err := os.Rename(exe, old); err != nil {
		return "", fmt.Errorf("moving %s aside to replace it: %w; nothing was installed", exe, err)
	}
	return old, nil
}

// removeOld removes the exe.old-* files moveAside left; one that still
// runs can't be removed on Windows and is left for a later update.
func removeOld(exe string) {
	olds, _ := filepath.Glob(exe + ".old-*")
	for _, o := range olds {
		_ = os.Remove(o)
	}
}
