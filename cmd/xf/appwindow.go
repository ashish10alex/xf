package main

import (
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path"
	"path/filepath"
	"runtime"
	"strings"
	"syscall"

	"github.com/ashish10alex/xf/internal/config"
)

// An app window is a Chromium browser's window for one page, with no tabs
// or address bar (--app=URL). Unlike a tab it gets ctrl+n, ctrl+t and
// ctrl+w, which off a Mac a browser keeps for itself in a tab, so xf web's
// ctrl+n moves there as it does in the TUI.

// appBrowser finds the browser webBrowser names for an app window: for
// "app" the first Chrome, Edge or Chromium found, else the path or name it
// gives. It returns "" when there is none, or webBrowser is "default".
func appBrowser(webBrowser string, env browserEnv) string {
	switch webBrowser {
	case "", config.BrowserDefault:
		return ""
	case config.BrowserApp:
		for _, c := range env.candidates() {
			if p := env.find(c); p != "" {
				return p
			}
		}
		return ""
	}
	return env.find(webBrowser)
}

// browserEnv is what appBrowser looks at: the OS, its environment and its
// files, so a test can give it another. wsl is Linux in WSL, whose
// browsers are Windows's, under /mnt/c; noInterop is WSL that can't run
// them.
type browserEnv struct {
	goos      string
	wsl       bool
	noInterop bool
	getenv    func(string) string
	exists    func(string) bool
	glob      func(string) ([]string, error)
	lookPath  func(string) (string, error)
}

func systemBrowserEnv() browserEnv {
	exists := func(p string) bool {
		_, err := os.Stat(p)
		return err == nil
	}
	wsl := runtime.GOOS == "linux" && inWSL(os.Getenv, os.ReadFile)
	return browserEnv{
		goos:      runtime.GOOS,
		wsl:       wsl,
		noInterop: wsl && !wslInterop(exists),
		getenv:    os.Getenv,
		exists:    exists,
		glob:      filepath.Glob,
		lookPath:  exec.LookPath,
	}
}

// inWSL reports whether Linux runs in WSL: WSL sets WSL_DISTRO_NAME, and
// its kernel's release names Microsoft.
func inWSL(getenv func(string) string, readFile func(string) ([]byte, error)) bool {
	if getenv("WSL_DISTRO_NAME") != "" {
		return true
	}
	b, err := readFile("/proc/sys/kernel/osrelease")
	return err == nil && strings.Contains(strings.ToLower(string(b)), "microsoft")
}

// wslInterop reports whether WSL can run Windows programs. WSL 2 runs an
// .exe through a binfmt_misc rule, WSLInterop (WSLInterop-late in newer
// WSL), which WSL adds when it starts. It goes missing while WSL runs
// when something clears binfmt_misc, or never comes when interop is off
// in /etc/wsl.conf; Linux then refuses the .exe with "exec format error".
// Without binfmt_misc at all (WSL 1) it can't tell, so says yes.
func wslInterop(exists func(string) bool) bool {
	const dir = "/proc/sys/fs/binfmt_misc/"
	if !exists(dir + "register") {
		return true
	}
	return exists(dir+"WSLInterop") || exists(dir+"WSLInterop-late")
}

// wslInteropFix says that WSL can't run Windows programs and how to let it.
const wslInteropFix = `WSL can't run Windows programs here (no WSLInterop rule in /proc/sys/fs/binfmt_misc).
  Restart WSL, which adds the rule back: run wsl --shutdown from Windows, then reopen WSL.
  Or add it now, until WSL restarts:
    sudo sh -c 'echo :WSLInterop:M::MZ::/init:PF > /proc/sys/fs/binfmt_misc/register'
  If /etc/wsl.conf has enabled=false under [interop], remove that first.`

// wslBrowsers are Windows's Chrome, Edge and Chromium as WSL sees them on
// its C: drive, which a Linux process can run: Program Files first, then
// each user's own install.
func (e browserEnv) wslBrowsers() []string {
	var out []string
	for _, rel := range []string{"Google/Chrome/Application/chrome.exe", "Microsoft/Edge/Application/msedge.exe", "Chromium/Application/chrome.exe"} {
		out = append(out, "/mnt/c/Program Files/"+rel, "/mnt/c/Program Files (x86)/"+rel)
		if e.glob != nil {
			if users, err := e.glob("/mnt/c/Users/*/AppData/Local/" + rel); err == nil {
				out = append(out, users...)
			}
		}
	}
	return out
}

// candidates are where Chrome, Edge and Chromium are put, in the order
// they are tried: a path, or a name to look up on PATH.
func (e browserEnv) candidates() []string {
	switch e.goos {
	case "windows":
		var out []string
		for _, rel := range []string{`Google\Chrome\Application\chrome.exe`, `Microsoft\Edge\Application\msedge.exe`, `Chromium\Application\chrome.exe`} {
			for _, base := range []string{e.getenv("ProgramFiles"), e.getenv("ProgramFiles(x86)"), e.getenv("LOCALAPPDATA")} {
				if base != "" {
					out = append(out, base+`\`+rel)
				}
			}
		}
		return append(out, "chrome", "msedge")
	case "darwin":
		var out []string
		for _, app := range []string{"Google Chrome.app", "Microsoft Edge.app", "Chromium.app"} {
			out = append(out, "/Applications/"+app)
			if home := e.getenv("HOME"); home != "" {
				out = append(out, path.Join(home, "Applications", app))
			}
		}
		return out
	}
	linux := []string{"google-chrome", "google-chrome-stable", "chromium", "chromium-browser", "microsoft-edge", "microsoft-edge-stable"}
	if e.wsl && !e.noInterop {
		// Windows's browser, the one the user has; a Linux one in WSL
		// (shown by WSLg) only after it.
		return append(e.wslBrowsers(), linux...)
	}
	return linux
}

// find is the browser at path p, or the program named p on PATH, or "".
func (e browserEnv) find(p string) string {
	if strings.ContainsAny(p, `/\`) {
		if e.exists(p) {
			return p
		}
		return ""
	}
	if path, err := e.lookPath(p); err == nil {
		return path
	}
	return ""
}

// appWindowCommand is the command that opens url in an app window of
// browser: on a Mac an .app is opened with open, which hands the flag to
// the browser already running.
func appWindowCommand(goos, browser, url string) *exec.Cmd {
	if goos == "darwin" && strings.HasSuffix(strings.TrimSuffix(browser, "/"), ".app") {
		return exec.Command("open", "-na", browser, "--args", "--app="+url)
	}
	return exec.Command(browser, "--app="+url)
}

// pageOpener opens xf web's page: in an app window of the browser
// webBrowser names, or the system's browser. note says why it fell back to
// the system's browser, if it did; open is nil when there is no way to
// open one at all. Its errors are whole messages, ready to print.
func pageOpener(webBrowser string) (open func(url string) error, note string) {
	env := systemBrowserEnv()
	var notes []string
	if env.noInterop && webBrowser == config.BrowserApp {
		notes = append(notes, "Windows's Chrome and Edge are skipped: "+wslInteropFix)
	}
	tab := browserOpener()
	if browser := appBrowser(webBrowser, env); browser != "" {
		start := func(url string) error {
			// The browser outlives xf web, and ctrl+c here mustn't reach it.
			_, err := startDetached(appWindowCommand(runtime.GOOS, browser, url))
			if err != nil && env.wsl && errors.Is(err, syscall.ENOEXEC) {
				// A Windows browser named by path, which WSL can't run.
				err = fmt.Errorf("%w\n%s", err, wslInteropFix)
			}
			return err
		}
		return appWindowOrTab(start, tab), strings.Join(notes, "\n")
	}
	if n := notFoundNote(webBrowser, env.noInterop); n != "" {
		notes = append(notes, n)
	}
	if tab == nil {
		return nil, strings.Join(notes, "\n")
	}
	return func(url string) error {
		if err := tab(url); err != nil {
			return fmt.Errorf("could not open the browser: %w", err)
		}
		return nil
	}, strings.Join(notes, "\n")
}

// notFoundNote says that no browser webBrowser names was found for an app
// window, so the page opens in a tab; "" when webBrowser asks for none.
// skipped is WSL that skipped Windows's browsers, leaving Linux ones.
func notFoundNote(webBrowser string, skipped bool) string {
	const tab = " found for an app window, so the page opens in a tab, where ctrl+n is the browser's"
	switch {
	case webBrowser == "" || webBrowser == config.BrowserDefault:
		return ""
	case webBrowser == config.BrowserApp && skipped:
		return "no Linux Chrome, Edge or Chromium" + tab
	case webBrowser == config.BrowserApp:
		return "no Chrome, Edge or Chromium" + tab
	}
	return "no browser " + webBrowser + tab
}

// appWindowOrTab opens the page with start, an app window, and when that
// fails, in a tab with tab (nil when there is no system browser). Either
// way the error says the app window failed, and what happened next.
func appWindowOrTab(start, tab func(url string) error) func(url string) error {
	return func(url string) error {
		err := start(url)
		if err == nil {
			return nil
		}
		msg := "could not open an app window: " + err.Error()
		if tab == nil {
			return errors.New(msg)
		}
		if terr := tab(url); terr != nil {
			return fmt.Errorf("%s\ncould not open the page in a tab either: %v", msg, terr)
		}
		return errors.New(msg + "\nso the page opened in a tab, where ctrl+n is the browser's")
	}
}
