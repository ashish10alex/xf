package cmdrun

import "strings"

// GivesFlag says args, a tool's arguments, give one of the flags names
// ("--target", "-t"), as --name, --name=value or --no-name, before any
// "--".
func GivesFlag(args []string, names ...string) bool {
	for _, a := range args {
		if a == "--" {
			return false
		}
		a, _, _ = strings.Cut(a, "=")
		for _, n := range names {
			if a == n || (strings.HasPrefix(n, "--") && a == "--no-"+n[2:]) {
				return true
			}
		}
	}
	return false
}
