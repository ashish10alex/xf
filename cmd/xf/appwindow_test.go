package main

import (
	"errors"
	"flag"
	"os"
	"path"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/ashish10alex/xf/internal/config"
)

// fakeBrowsers is a browserEnv for goos with only the files and PATH
// programs given.
func fakeBrowsers(goos string, files, onPath []string) browserEnv {
	env := map[string]string{"ProgramFiles": `C:\Program Files`, "ProgramFiles(x86)": `C:\Program Files (x86)`, "LOCALAPPDATA": `C:\Users\a\AppData\Local`, "HOME": "/Users/a"}
	return browserEnv{
		goos:   goos,
		getenv: func(k string) string { return env[k] },
		exists: func(p string) bool { return slices.Contains(files, p) },
		lookPath: func(name string) (string, error) {
			if slices.Contains(onPath, name) {
				return "/usr/bin/" + name, nil
			}
			return "", errors.New("not found")
		},
	}
}

func TestAppBrowser(t *testing.T) {
	edge := `C:\Program Files (x86)\Microsoft\Edge\Application\msedge.exe`
	chrome := `C:\Users\a\AppData\Local\Google\Chrome\Application\chrome.exe`
	for _, c := range []struct {
		name, setting string
		env           browserEnv
		want          string
	}{
		{"default is no app window", config.BrowserDefault, fakeBrowsers("windows", []string{edge}, nil), ""},
		{"unset is no app window", "", fakeBrowsers("windows", []string{edge}, nil), ""},
		{"Windows with Edge only", config.BrowserApp, fakeBrowsers("windows", []string{edge}, nil), edge},
		{"Windows: Chrome before Edge", config.BrowserApp, fakeBrowsers("windows", []string{edge, chrome}, nil), chrome},
		{"Windows with neither", config.BrowserApp, fakeBrowsers("windows", nil, nil), ""},
		{"Mac", config.BrowserApp, fakeBrowsers("darwin", []string{"/Applications/Microsoft Edge.app"}, nil), "/Applications/Microsoft Edge.app"},
		{"Mac, in ~/Applications", config.BrowserApp, fakeBrowsers("darwin", []string{"/Users/a/Applications/Chromium.app"}, nil), "/Users/a/Applications/Chromium.app"},
		{"Linux: Chromium before Edge", config.BrowserApp, fakeBrowsers("linux", nil, []string{"microsoft-edge", "chromium"}), "/usr/bin/chromium"},
		{"a name on PATH", "brave", fakeBrowsers("linux", nil, []string{"brave"}), "/usr/bin/brave"},
		{"a path", `D:\b\chrome.exe`, fakeBrowsers("windows", []string{`D:\b\chrome.exe`}, nil), `D:\b\chrome.exe`},
		{"a path that isn't there", "/opt/none/chrome", fakeBrowsers("linux", nil, []string{"chromium"}), ""},
	} {
		if got := appBrowser(c.setting, c.env); got != c.want {
			t.Errorf("%s: appBrowser(%q) = %q, want %q", c.name, c.setting, got, c.want)
		}
	}
}

func TestAppWindowCommand(t *testing.T) {
	url := "http://127.0.0.1:5000/?project=a"
	if got := appWindowCommand("darwin", "/Applications/Google Chrome.app", "", url).Args; !slices.Equal(got, []string{"open", "-na", "/Applications/Google Chrome.app", "--args", "--app=" + url}) {
		t.Errorf("Mac .app = %q", got)
	}
	if got := appWindowCommand("windows", `C:\e\msedge.exe`, "", url).Args; !slices.Equal(got, []string{`C:\e\msedge.exe`, "--app=" + url}) {
		t.Errorf("Windows = %q", got)
	}
	if got := appWindowCommand("darwin", "/Applications/Google Chrome.app", "Profile 1", url).Args; !slices.Equal(got, []string{"open", "-na", "/Applications/Google Chrome.app", "--args", "--profile-directory=Profile 1", "--app=" + url}) {
		t.Errorf("Mac .app with a profile = %q", got)
	}
}

func TestLinkCommand(t *testing.T) {
	url := "https://gitlab.com/a/b/-/merge_requests/1"
	// On a Mac, open -a with no profile: a command line (open -na, or the
	// .app's own program) opened a new window when the app window was the
	// last one used.
	if got := linkCommand("darwin", "/Applications/Google Chrome.app", "Profile 1", url).Args; !slices.Equal(got, []string{"open", "-a", "/Applications/Google Chrome.app", url}) {
		t.Errorf("Mac .app = %q", got)
	}
	if got := linkCommand("linux", "/usr/bin/chromium", "Default", url).Args; !slices.Equal(got, []string{"/usr/bin/chromium", "--profile-directory=Default", url}) {
		t.Errorf("Linux = %q", got)
	}
}

func TestWebBrowser(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home) // what os.UserHomeDir reads on Windows
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(home, "cfg"))
	userFile := filepath.Join(home, "cfg", "xf", "config.json")
	if err := os.MkdirAll(filepath.Dir(userFile), 0o755); err != nil {
		t.Fatal(err)
	}
	get := func(user string, app bool, args ...string) string {
		t.Helper()
		if err := os.WriteFile(userFile, []byte(user), 0o644); err != nil {
			t.Fatal(err)
		}
		fs := flag.NewFlagSet("xf web", flag.ContinueOnError)
		sf := addSettingFlags(fs, config.Keys...)
		if err := fs.Parse(args); err != nil {
			t.Fatal(err)
		}
		b, _ := webBrowser(sf, app)
		return b
	}
	if b := get(`{}`, false); b != "" {
		t.Errorf("nothing set = %q, want the system's browser", b)
	}
	if b := get(`{}`, true); b != config.BrowserApp {
		t.Errorf("--app = %q", b)
	}
	if b := get(`{"webBrowser": "default"}`, true); b != config.BrowserApp {
		t.Errorf("--app over default = %q", b)
	}
	if b := get(`{"webBrowser": "msedge"}`, true); b != "msedge" {
		t.Errorf("--app with a browser set = %q, want the browser", b)
	}
	if b := get(`{"webBrowser": "app"}`, false, "--web-browser", "default"); b != config.BrowserDefault {
		t.Errorf("--web-browser default over the file = %q", b)
	}
	if b := get(`{"webBrowser": "~/bin/chrome"}`, false); b != filepath.Join(home, "bin", "chrome") {
		t.Errorf("~ in the file = %q", b)
	}
	if err := os.WriteFile(userFile, []byte(`{"webBrowserProfile": "Profile 1"}`), 0o644); err != nil {
		t.Fatal(err)
	}
	fs := flag.NewFlagSet("xf web", flag.ContinueOnError)
	sf := addSettingFlags(fs, config.Keys...)
	if _, p := webBrowser(sf, true); p != "Profile 1" {
		t.Errorf("profile from the file = %q", p)
	}
}

func noInterop(env browserEnv) browserEnv {
	env.noInterop = true
	return env
}

func TestAppBrowserInWSL(t *testing.T) {
	edge := "/mnt/c/Program Files (x86)/Microsoft/Edge/Application/msedge.exe"
	userChrome := "/mnt/c/Users/ash/AppData/Local/Google/Chrome/Application/chrome.exe"
	wsl := func(files, onPath []string) browserEnv {
		env := fakeBrowsers("linux", files, onPath)
		env.wsl = true
		env.glob = func(pattern string) ([]string, error) {
			var out []string
			for _, f := range files {
				if ok, _ := path.Match(pattern, f); ok {
					out = append(out, f)
				}
			}
			return out, nil
		}
		return env
	}
	for _, c := range []struct {
		name string
		env  browserEnv
		want string
	}{
		{"Windows's Edge", wsl([]string{edge}, nil), edge},
		{"a user's own Chrome before Edge", wsl([]string{edge, userChrome}, nil), userChrome},
		{"Windows's before a Linux one", wsl([]string{edge}, []string{"chromium"}), edge},
		{"a Linux one when Windows has none", wsl(nil, []string{"chromium"}), "/usr/bin/chromium"},
		{"none", wsl(nil, nil), ""},
		{"no interop: a Linux one, not Windows's", noInterop(wsl([]string{edge}, []string{"chromium"})), "/usr/bin/chromium"},
		{"no interop and no Linux one", noInterop(wsl([]string{edge}, nil)), ""},
	} {
		if got := appBrowser(config.BrowserApp, c.env); got != c.want {
			t.Errorf("%s: appBrowser = %q, want %q", c.name, got, c.want)
		}
	}
	// Plain Linux never looks on a C: drive.
	if got := appBrowser(config.BrowserApp, fakeBrowsers("linux", []string{edge}, nil)); got != "" {
		t.Errorf("Linux outside WSL = %q", got)
	}
}

func TestInWSL(t *testing.T) {
	env := func(m map[string]string) func(string) string { return func(k string) string { return m[k] } }
	release := func(s string) func(string) ([]byte, error) {
		return func(string) ([]byte, error) { return []byte(s), nil }
	}
	if !inWSL(env(map[string]string{"WSL_DISTRO_NAME": "Ubuntu"}), release("6.8.0-45-generic")) {
		t.Error("WSL_DISTRO_NAME set: not WSL")
	}
	if !inWSL(env(nil), release("5.15.167.4-microsoft-standard-WSL2\n")) {
		t.Error("a Microsoft kernel: not WSL")
	}
	if inWSL(env(nil), release("6.8.0-45-generic\n")) {
		t.Error("an Ubuntu kernel: WSL")
	}
}

func TestWSLInterop(t *testing.T) {
	const dir = "/proc/sys/fs/binfmt_misc/"
	for _, c := range []struct {
		name  string
		files []string
		want  bool
	}{
		{"WSLInterop", []string{dir + "register", dir + "WSLInterop"}, true},
		{"WSLInterop-late", []string{dir + "register", dir + "WSLInterop-late"}, true},
		{"cleared by systemd, or off", []string{dir + "register", dir + "status"}, false},
		{"no binfmt_misc (WSL 1)", nil, true},
	} {
		if got := wslInterop(func(p string) bool { return slices.Contains(c.files, p) }); got != c.want {
			t.Errorf("%s: wslInterop = %v, want %v", c.name, got, c.want)
		}
	}
}

func TestAppWindowOrTab(t *testing.T) {
	var tabbed []string
	tab := func(url string) error { tabbed = append(tabbed, url); return nil }
	ok := func(string) error { return nil }
	fail := func(string) error { return errors.New("exec format error") }

	if err := appWindowOrTab(ok, tab)("u"); err != nil || tabbed != nil {
		t.Errorf("app window opened: err %v, tabs %q", err, tabbed)
	}
	err := appWindowOrTab(fail, tab)("u")
	if !slices.Equal(tabbed, []string{"u"}) {
		t.Errorf("app window failed: tabs %q, want the page in one", tabbed)
	}
	if err == nil || !strings.Contains(err.Error(), "exec format error") || !strings.Contains(err.Error(), "opened in a tab") {
		t.Errorf("app window failed, tab opened: err %v", err)
	}
	err = appWindowOrTab(fail, func(string) error { return errors.New("no xdg-open") })("u")
	if err == nil || !strings.Contains(err.Error(), "exec format error") || !strings.Contains(err.Error(), "no xdg-open") {
		t.Errorf("both failed: err %v", err)
	}
	if err := appWindowOrTab(fail, nil)("u"); err == nil || !strings.Contains(err.Error(), "exec format error") {
		t.Errorf("no system browser: err %v", err)
	}
}

func TestNotFoundNote(t *testing.T) {
	for _, c := range []struct {
		setting string
		skipped bool
		want    string
	}{
		{config.BrowserDefault, false, ""},
		{"", true, ""},
		{config.BrowserApp, false, "no Chrome, Edge or Chromium found"},
		{config.BrowserApp, true, "no Linux Chrome, Edge or Chromium found"},
		{"brave", false, "no browser brave found"},
	} {
		got := notFoundNote(c.setting, c.skipped)
		if (c.want == "") != (got == "") || !strings.HasPrefix(got, c.want) {
			t.Errorf("notFoundNote(%q, %v) = %q, want it to start %q", c.setting, c.skipped, got, c.want)
		}
	}
}
