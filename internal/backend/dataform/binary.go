package dataform

import (
	"errors"
	"fmt"
	"os"
	"os/exec"
	"strings"
)

// BinEnv is the environment variable that points at the dataform binary.
const BinEnv = "DATAFORM_BIN"

// InstallCommand installs the Dataform CLI.
const InstallCommand = "npm i -g @dataform/cli"

// ErrNotOnPath is what ResolveBinary's error is when no binary was named
// and "dataform" isn't on PATH, which installing the CLI fixes.
var ErrNotOnPath = errors.New(`"dataform" isn't on PATH`)

// notOnPath is ErrNotOnPath with the whole message.
type notOnPath struct{ msg string }

func (e notOnPath) Error() string      { return e.msg }
func (notOnPath) Is(target error) bool { return target == ErrNotOnPath }

// ResolveBinary finds the dataform binary: the --dataform-bin flag first, then
// $DATAFORM_BIN, then "dataform" on PATH. getenv and lookPath are injected so
// tests don't depend on the real environment (pass os.Getenv, exec.LookPath).
func ResolveBinary(flag string, getenv func(string) string, lookPath func(string) (string, error)) (string, error) {
	return ResolveBinaryFrom(flag, "--dataform-bin", getenv, lookPath)
}

// ResolveBinaryFrom is ResolveBinary for a path from somewhere other than
// the flag (a config file, say): source names it in errors.
func ResolveBinaryFrom(bin, source string, getenv func(string) string, lookPath func(string) (string, error)) (string, error) {
	candidate := "dataform"
	if bin != "" {
		candidate = bin
	} else if env := getenv(BinEnv); env != "" {
		candidate, source = env, "$"+BinEnv
	} else {
		source = "PATH"
	}

	path, err := lookPath(candidate)
	if err != nil {
		var hint string
		if strings.ContainsAny(candidate, `/\`) {
			hint = fmt.Sprintf("could not find a dataform binary at %q (from %s)", candidate, source)
		} else {
			hint = fmt.Sprintf("could not find %q on PATH", candidate)
			if source != "PATH" {
				hint += " (from " + source + ")"
			}
		}
		msg := fmt.Sprintf("%s. Install the Dataform CLI (%s), or set --dataform-bin, $%s or \"dataformBin\" in .xf/config.json", hint, InstallCommand, BinEnv)
		if source == "PATH" {
			return "", notOnPath{msg}
		}
		return "", errors.New(msg)
	}
	return path, nil
}

// which is bin, or when bin is "" the dataform binary on PATH now: a
// Backend opened before the CLI was installed finds it once it is.
func which(bin string) (string, error) {
	if bin != "" {
		return bin, nil
	}
	return ResolveBinary("", os.Getenv, exec.LookPath)
}
