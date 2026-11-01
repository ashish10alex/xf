package cmdrun

import (
	"os"
	"strconv"
)

// ctrlCArg is the argument that runs this program as the helper that sends
// a Windows process the console Ctrl+C of a graceful cancel.
const ctrlCArg = "__ctrlc"

// Every program that runs tools through this package, xf and its tests
// alike, can be its own Ctrl+C helper: run as `<exe> __ctrlc <pid>`, it
// does that and exits before main.
func init() {
	if len(os.Args) == 3 && os.Args[1] == ctrlCArg {
		pid, err := strconv.Atoi(os.Args[2])
		if err != nil {
			os.Exit(2)
		}
		os.Exit(sendCtrlC(pid))
	}
}
