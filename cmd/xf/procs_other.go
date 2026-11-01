//go:build !darwin && !linux && !windows

package main

import (
	"errors"
	"runtime"
)

func listProcs() ([]proc, error) {
	return nil, errors.New("xf stop can't list processes on " + runtime.GOOS)
}
