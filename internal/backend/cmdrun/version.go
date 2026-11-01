package cmdrun

import (
	"fmt"
	"strings"
)

// FirstLine is the first line of out that isn't blank, trimmed: what a
// tool's --version prints.
func FirstLine(out string) string {
	for _, l := range strings.Split(out, "\n") {
		if l = strings.TrimSpace(l); l != "" {
			return l
		}
	}
	return ""
}

// VersionError is the error of a tool's --version that failed with err
// after printing out: what it printed first, if anything.
func VersionError(out []byte, err error) error {
	if l := FirstLine(StripANSI(string(out))); l != "" {
		return fmt.Errorf("%s (%w)", l, err)
	}
	return err
}
