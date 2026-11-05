package main

import (
	"archive/tar"
	"archive/zip"
	"bytes"
	"compress/gzip"
	"crypto/sha256"
	"encoding/hex"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"runtime/debug"
	"strings"
	"testing"
)

func TestDetectInstall(t *testing.T) {
	release := &debug.BuildInfo{Main: debug.Module{Version: "v0.0.2"}}
	goInstalled := &debug.BuildInfo{Main: debug.Module{Version: "v0.0.2", Sum: "h1:abc="}}
	devel := &debug.BuildInfo{Main: debug.Module{Version: "(devel)"}}
	pseudo := &debug.BuildInfo{Main: debug.Module{Version: "v0.0.3-0.20261105232800-b33e174a1b2c"}}
	dirty := &debug.BuildInfo{Main: debug.Module{Version: "v0.0.2+dirty"}}
	for _, tc := range []struct {
		exe  string
		bi   *debug.BuildInfo
		want installMethod
	}{
		{"/opt/homebrew/Caskroom/xf/0.0.2/xf", release, installHomebrew},
		{"/home/linuxbrew/.linuxbrew/Caskroom/xf/0.0.2/xf", release, installHomebrew},
		{`C:\Users\a\scoop\apps\xf\0.0.2\xf.exe`, release, installScoop},
		{`C:\Users\a\AppData\Local\Microsoft\WinGet\Packages\AshishAlex.xf_Microsoft.Winget.Source_8wekyb3d8bbwe\xf.exe`, release, installWinget},
		{"/Users/a/go/bin/xf", goInstalled, installGo},
		{"/Users/a/.local/bin/xf", release, installRelease},
		{`C:\Users\a\AppData\Local\Programs\xf\xf.exe`, release, installRelease},
		{"/Users/a/go/bin/xf", devel, installSource},
		{"/Users/a/go/bin/xf", pseudo, installSource},
		{"/Users/a/go/bin/xf", dirty, installSource},
		{"/Users/a/go/bin/xf", nil, installSource},
	} {
		if got := detectInstall(tc.exe, tc.bi); got != tc.want {
			t.Errorf("detectInstall(%s, %+v) = %d, want %d", tc.exe, tc.bi, got, tc.want)
		}
	}
}

// fakeRelease serves a release like GitHub's: releases/latest redirects to
// the tag, whose download holds the archive and checksums.txt.
func fakeRelease(t *testing.T, tag, archive string, data []byte, sum string) {
	t.Helper()
	if sum == "" {
		h := sha256.Sum256(data)
		sum = hex.EncodeToString(h[:])
	}
	mux := http.NewServeMux()
	mux.HandleFunc("/releases/latest", func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, "/releases/tag/"+tag, http.StatusFound)
	})
	mux.HandleFunc("/releases/download/"+tag+"/"+archive, func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write(data)
	})
	mux.HandleFunc("/releases/download/"+tag+"/checksums.txt", func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte("0000  xf_other.tar.gz\n" + sum + "  " + archive + "\n"))
	})
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	old := releasesURL
	releasesURL = srv.URL + "/releases"
	t.Cleanup(func() { releasesURL = old })
}

func tarGz(t *testing.T, name string, content []byte) []byte {
	t.Helper()
	var b bytes.Buffer
	gz := gzip.NewWriter(&b)
	tw := tar.NewWriter(gz)
	for _, f := range []struct {
		name string
		body []byte
	}{{"LICENSE", []byte("MIT")}, {name, content}} {
		if err := tw.WriteHeader(&tar.Header{Name: f.name, Mode: 0o755, Size: int64(len(f.body)), Typeflag: tar.TypeReg}); err != nil {
			t.Fatal(err)
		}
		_, _ = tw.Write(f.body)
	}
	_ = tw.Close()
	_ = gz.Close()
	return b.Bytes()
}

func zipOf(t *testing.T, name string, content []byte) []byte {
	t.Helper()
	var b bytes.Buffer
	zw := zip.NewWriter(&b)
	f, err := zw.Create(name)
	if err != nil {
		t.Fatal(err)
	}
	_, _ = f.Write(content)
	_ = zw.Close()
	return b.Bytes()
}

func TestExtractXF(t *testing.T) {
	if got, err := extractXF(tarGz(t, "xf", []byte("new xf")), "xf_linux_amd64.tar.gz"); err != nil || string(got) != "new xf" {
		t.Errorf("tar.gz: %q, %v", got, err)
	}
	if got, err := extractXF(zipOf(t, "xf.exe", []byte("new xf.exe")), "xf_windows_arm64.zip"); err != nil || string(got) != "new xf.exe" {
		t.Errorf("zip: %q, %v", got, err)
	}
	if _, err := extractXF(tarGz(t, "README.md", nil), "xf_linux_amd64.tar.gz"); err == nil {
		t.Error("an archive without xf: no error")
	}
}

// selfUpdateArchive is the release archive this OS and arch would get.
func selfUpdateArchive(t *testing.T, content []byte) (string, []byte) {
	name := "xf_" + runtime.GOOS + "_" + runtime.GOARCH
	if runtime.GOOS == "windows" {
		return name + ".zip", zipOf(t, "xf.exe", content)
	}
	return name + ".tar.gz", tarGz(t, "xf", content)
}

func TestSelfUpdate(t *testing.T) {
	exe := filepath.Join(t.TempDir(), "xf")
	if runtime.GOOS == "windows" {
		exe += ".exe"
	}
	if err := os.WriteFile(exe, []byte("old xf"), 0o755); err != nil {
		t.Fatal(err)
	}
	archive, data := selfUpdateArchive(t, []byte("new xf"))
	fakeRelease(t, "v0.0.3", archive, data, "")

	var out strings.Builder
	if err := selfUpdate(&out, http.DefaultClient, exe, "v0.0.3", runtime.GOOS, runtime.GOARCH); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out.String(), "v0.0.3 is the latest") {
		t.Errorf("on the latest release, said %q", out.String())
	}
	if got, _ := os.ReadFile(exe); string(got) != "old xf" {
		t.Errorf("on the latest release, xf was replaced: %q", got)
	}

	out.Reset()
	if err := selfUpdate(&out, http.DefaultClient, exe, "v0.0.2", runtime.GOOS, runtime.GOARCH); err != nil {
		t.Fatal(err)
	}
	if got, _ := os.ReadFile(exe); string(got) != "new xf" {
		t.Errorf("after the update, xf holds %q", got)
	}
	if fi, err := os.Stat(exe); err != nil || (runtime.GOOS != "windows" && fi.Mode().Perm()&0o111 == 0) {
		t.Errorf("the new xf isn't executable: %v, %v", fi.Mode(), err)
	}
	if !strings.Contains(out.String(), "from v0.0.2 to v0.0.3") {
		t.Errorf("said %q", out.String())
	}
	if left, _ := filepath.Glob(filepath.Join(filepath.Dir(exe), "*")); len(left) != 1 {
		t.Errorf("files left beside xf: %v", left)
	}
}

func TestSelfUpdateChecksumMismatch(t *testing.T) {
	exe := filepath.Join(t.TempDir(), "xf")
	if err := os.WriteFile(exe, []byte("old xf"), 0o755); err != nil {
		t.Fatal(err)
	}
	archive, data := selfUpdateArchive(t, []byte("new xf"))
	fakeRelease(t, "v0.0.3", archive, data, strings.Repeat("ab", 32))

	err := selfUpdate(&strings.Builder{}, http.DefaultClient, exe, "v0.0.2", runtime.GOOS, runtime.GOARCH)
	if err == nil || !strings.Contains(err.Error(), "doesn't match checksums.txt") {
		t.Errorf("a bad checksum gave %v", err)
	}
	if got, _ := os.ReadFile(exe); string(got) != "old xf" {
		t.Errorf("a bad checksum replaced xf: %q", got)
	}
}

func TestDownloadRefusesAWebPage(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte("<!doctype html><title>Checking this file</title>"))
	}))
	defer srv.Close()
	if _, err := download(http.DefaultClient, srv.URL+"/xf_linux_amd64.tar.gz"); err == nil || !strings.Contains(err.Error(), "web page") {
		t.Errorf("a proxy's page gave %v", err)
	}
}

func TestLatestReleaseNeedsARedirect(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte("<html>sign in</html>"))
	}))
	defer srv.Close()
	old := releasesURL
	releasesURL = srv.URL + "/releases"
	defer func() { releasesURL = old }()
	if tag, err := latestRelease(http.DefaultClient); err == nil {
		t.Errorf("a page instead of a redirect gave tag %q", tag)
	}
}

func TestUpdateFromSource(t *testing.T) {
	bi := &debug.BuildInfo{Main: debug.Module{Version: "(devel)"}}
	err := update(&strings.Builder{}, "/Users/a/go/bin/xf", bi, installSource)
	if err == nil || !strings.Contains(err.Error(), "just install") {
		t.Errorf("updating a source build gave %v", err)
	}
}
