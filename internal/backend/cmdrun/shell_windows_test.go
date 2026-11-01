//go:build windows

package cmdrun

import (
	"encoding/json"
	"os/exec"
	"slices"
	"strings"
	"syscall"
	"testing"

	"github.com/ashish10alex/xf/internal/testutil"
)

// A command line ShellJoin shows pastes into PowerShell and into cmd and
// gives the program the same arguments in both.
func TestShellJoinPastesIntoPowerShellAndCmd(t *testing.T) {
	echo := testutil.FakeCLI(t, "xf-echo")
	args := []string{"build", "--select", "+shop.marts.orders+", "my file.sql", "x=1", "a&b", "--vars", "{env: dev}"}
	line := ShellJoin(append([]string{echo}, args...))
	ps := exec.Command("powershell", "-NoProfile", "-Command", "& "+line)
	// cmd gets the line exactly as pasted: Go would re-quote an argument.
	cmd := exec.Command("cmd")
	cmd.SysProcAttr = &syscall.SysProcAttr{CmdLine: "cmd /c " + line}
	for name, c := range map[string]*exec.Cmd{"powershell": ps, "cmd": cmd} {
		out, err := c.CombinedOutput()
		if err != nil {
			t.Fatalf("%s: %v\n%s", name, err, out)
		}
		var got []string
		if err := json.Unmarshal([]byte(strings.TrimSpace(string(out))), &got); err != nil || !slices.Equal(got, args) {
			t.Errorf("%s ran %s with %q, want %q (%v)", name, line, got, args, err)
		}
	}
}
