package dataform

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
)

// NpmBundle finds the script an npm Windows shim for the Dataform CLI runs:
// npm installs `dataform` as dataform.cmd beside node_modules, and the
// shim runs node on node_modules/@dataform/cli/bundle.js. It returns ""
// when bin isn't such a shim.
func NpmBundle(bin string) string {
	if !strings.EqualFold(filepath.Ext(bin), ".cmd") {
		return ""
	}
	bundle := filepath.Join(filepath.Dir(bin), "node_modules", "@dataform", "cli", "bundle.js")
	if _, err := os.Stat(bundle); err != nil {
		return ""
	}
	return bundle
}

// command is what to run for the dataform binary bin: bin itself, or for
// an npm shim, node on the CLI's bundle (the node beside the shim, as the
// shim prefers, else the one on PATH). Running node directly keeps cmd.exe
// out of the process tree, so a Ctrl+C reaches the CLI as from a terminal.
// A bin of "" is the one on PATH now (see which), an error when there is
// none.
func command(bin string) (prog string, prefix []string, err error) {
	if bin, err = which(bin); err != nil {
		return "", nil, err
	}
	bundle := NpmBundle(bin)
	if bundle == "" {
		return bin, nil, nil
	}
	node := filepath.Join(filepath.Dir(bin), "node.exe")
	if _, err := os.Stat(node); err != nil {
		if node, err = exec.LookPath("node"); err != nil {
			return bin, nil, nil
		}
	}
	return node, []string{bundle}, nil
}
