// Command fakecli stands in for the dataform and dbt CLIs (and a couple of
// other programs) in xf's tests, behaving as the one it's named after; see
// testutil.FakeCLI, which builds it. It's a real program rather than the
// test binary because Go only delivers a console Ctrl+C on Windows once
// main has started.
package main

import (
	"bufio"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"strconv"
	"strings"
	"time"
)

var fakes = map[string]func(args []string) int{
	"dataform": fakeDataform,
	"dbt":      fakeDBT,
	"xf-wait":  fakeWait,
	"xf-fail":  func([]string) int { return 1 },
	"xf-echo":  fakeEcho,
	"gcloud":   fakeGcloud,
}

// fakeEcho prints the arguments it was given as JSON, to check how a shell
// split a command line.
func fakeEcho(args []string) int {
	data, _ := json.Marshal(args)
	fmt.Println(string(data))
	return 0
}

func main() {
	name := strings.TrimSuffix(filepath.Base(os.Args[0]), ".exe")
	fake, ok := fakes[name]
	if !ok {
		fmt.Fprintf(os.Stderr, "fakecli: no fake called %q\n", name)
		os.Exit(2)
	}
	os.Exit(fake(os.Args[1:]))
}

// fakeDataform stands in for the dataform CLI.
//
//	--version: prints 3.0.8.
//	compile: prints the file $FAKE_COMPILE_FIXTURE names (nothing when it's
//	         unset), $FAKE_COMPILE_STDERR on stderr, and exits
//	         $FAKE_COMPILE_EXIT. With $FAKE_COMPILE_CALLS, a file it counts
//	         its calls in, the first $FAKE_COMPILE_RACES calls instead fail
//	         as dataform does when git removes .git/index.lock while it
//	         copies the project.
//	run:     echoes its args and a few lines (the first in colour), waiting
//	         $FAKE_RUN_SLEEP seconds before each step, then "error line" on
//	         stderr, and exits $FAKE_RUN_EXIT. An interrupt prints
//	         "interrupted" and exits 130, or with $FAKE_RUN_SLOW_CANCEL
//	         prints "cancelling" and carries on, as dataform does while it
//	         waits for its BigQuery jobs. With $FAKE_RUN_GRANDCHILD it first
//	         starts a child that shares its output and outlives it unless
//	         the whole process tree is stopped.
func fakeDataform(args []string) int {
	if len(args) == 0 {
		fmt.Fprintln(os.Stderr, "fake dataform: no command")
		return 2
	}
	switch args[0] {
	case "--version":
		fmt.Println("3.0.8")
		return 0
	case "compile":
		if f := os.Getenv("FAKE_COMPILE_CALLS"); f != "" {
			data, _ := os.ReadFile(f)
			calls := len(data) + 1
			if err := os.WriteFile(f, append(data, '.'), 0o644); err != nil {
				fmt.Fprintln(os.Stderr, err)
				return 2
			}
			if calls <= envInt("FAKE_COMPILE_RACES") {
				fmt.Fprintln(os.Stderr, "Dataform encountered an error: ENOENT: no such file or directory, stat '/p/.git/index.lock'")
				return 1
			}
		}
		if f := os.Getenv("FAKE_COMPILE_FIXTURE"); f != "" {
			data, err := os.ReadFile(f)
			if err != nil {
				fmt.Fprintln(os.Stderr, err)
				return 2
			}
			_, _ = os.Stdout.Write(data)
		}
		if s := os.Getenv("FAKE_COMPILE_STDERR"); s != "" {
			fmt.Fprintln(os.Stderr, s)
		}
		return envInt("FAKE_COMPILE_EXIT")
	case "run":
		interrupt := make(chan os.Signal, 1)
		signal.Notify(interrupt, os.Interrupt)
		if os.Getenv("FAKE_RUN_GRANDCHILD") != "" {
			child := exec.Command(os.Args[0], "__sleep", "30")
			child.Stdout, child.Stderr = os.Stdout, os.Stderr
			if err := child.Start(); err != nil {
				fmt.Fprintln(os.Stderr, err)
				return 2
			}
		}
		fmt.Println("args: " + strings.Join(args, " "))
		fmt.Println("\x1b[32mCompiling...\x1b[0m")
		sleep := envSeconds("FAKE_RUN_SLEEP")
		for i := 1; i <= 3; i++ {
			deadline := time.After(sleep)
		wait:
			for {
				select {
				case <-interrupt:
					if os.Getenv("FAKE_RUN_SLOW_CANCEL") == "" {
						fmt.Println("interrupted")
						return 130
					}
					fmt.Println("cancelling")
				case <-deadline:
					break wait
				}
			}
			fmt.Printf("step %d\n", i)
		}
		fmt.Fprintln(os.Stderr, "error line")
		return envInt("FAKE_RUN_EXIT")
	case "__sleep":
		// The grandchild FAKE_RUN_GRANDCHILD starts: it only holds the
		// output open.
		n, _ := strconv.Atoi(args[1])
		time.Sleep(time.Duration(n) * time.Second)
		return 0
	}
	fmt.Fprintf(os.Stderr, "fake dataform: unsupported command %s\n", args[0])
	return 2
}

// fakeDBT stands in for the dbt CLI.
//
//	--version:      prints $FAKE_DBT_VERSION (default "dbt 2.0.6").
//	parse, compile: appends the command to $FAKE_DBT_CALLS if set (as
//	                command:target when --target is given), prints
//	                $FAKE_DBT_LOG (JSON log lines) and $FAKE_DBT_STDERR,
//	                copies $FAKE_DBT_PARSE or $FAKE_DBT_COMPILE into
//	                <--target-path>/manifest.json when set, and exits
//	                $FAKE_DBT_PARSE_EXIT or $FAKE_DBT_COMPILE_EXIT.
//	                With $FAKE_DBT_ARGS it writes its arguments beside the
//	                manifest, as args.
//	build:          prints its arguments and working directory, two lines of
//	                progress (the first in colour), then exits
//	                $FAKE_DBT_BUILD_EXIT.
func fakeDBT(args []string) int {
	if len(args) == 0 {
		fmt.Fprintln(os.Stderr, "fake dbt: no command")
		return 2
	}
	if args[0] == "--version" {
		v := os.Getenv("FAKE_DBT_VERSION")
		if v == "" {
			v = "dbt 2.0.6"
		}
		fmt.Println(v)
		return 0
	}
	command, rest := args[0], args[1:]
	if command == "build" {
		fmt.Println("args: build " + strings.Join(rest, " "))
		dir, _ := os.Getwd()
		fmt.Println("dir: " + dir)
		fmt.Println("\x1b[32m1 of 1 OK\x1b[0m")
		fmt.Println("Done.")
		return envInt("FAKE_DBT_BUILD_EXIT")
	}
	var targetPath, target string
	for i := 0; i < len(rest); i++ {
		switch rest[i] {
		case "--target-path":
			i++
			targetPath = rest[i]
		case "--target":
			i++
			target = rest[i]
		}
	}
	if calls := os.Getenv("FAKE_DBT_CALLS"); calls != "" {
		entry := command
		if target != "" {
			entry += ":" + target
		}
		f, err := os.OpenFile(calls, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o644)
		if err != nil {
			fmt.Fprintln(os.Stderr, err)
			return 2
		}
		_, _ = fmt.Fprintln(f, entry)
		_ = f.Close()
	}
	if s := os.Getenv("FAKE_DBT_LOG"); s != "" {
		fmt.Println(s)
	}
	if s := os.Getenv("FAKE_DBT_STDERR"); s != "" {
		fmt.Fprintln(os.Stderr, s)
	}
	var manifest string
	var code int
	switch command {
	case "parse":
		manifest, code = os.Getenv("FAKE_DBT_PARSE"), envInt("FAKE_DBT_PARSE_EXIT")
	case "compile":
		manifest, code = os.Getenv("FAKE_DBT_COMPILE"), envInt("FAKE_DBT_COMPILE_EXIT")
	default:
		fmt.Fprintf(os.Stderr, "fake dbt: unknown command %s\n", command)
		return 2
	}
	if manifest != "" {
		data, err := os.ReadFile(manifest)
		if err == nil {
			err = os.MkdirAll(targetPath, 0o755)
		}
		if err == nil {
			err = os.WriteFile(filepath.Join(targetPath, "manifest.json"), data, 0o644)
		}
		if err == nil && os.Getenv("FAKE_DBT_ARGS") != "" {
			err = os.WriteFile(filepath.Join(targetPath, "args"), []byte(strings.Join(args, " ")), 0o644)
		}
		if err != nil {
			fmt.Fprintln(os.Stderr, err)
			return 2
		}
	}
	return code
}

// fakeGcloud stands in for gcloud config list: it prints
// $FAKE_GCLOUD_CONFIG, or with $FAKE_GCLOUD_ERROR prints that on stderr and
// exits 1. gcloud auth print-access-token prints a token, or with
// $FAKE_GCLOUD_TOKEN_ERROR prints that on stderr and exits 1.
func fakeGcloud(args []string) int {
	switch strings.Join(args, " ") {
	case "config list --format=json":
		if msg := os.Getenv("FAKE_GCLOUD_ERROR"); msg != "" {
			fmt.Fprintln(os.Stderr, msg)
			return 1
		}
		fmt.Println(os.Getenv("FAKE_GCLOUD_CONFIG"))
		return 0
	case "auth print-access-token":
		if msg := os.Getenv("FAKE_GCLOUD_TOKEN_ERROR"); msg != "" {
			fmt.Fprintln(os.Stderr, msg)
			return 1
		}
		fmt.Println("ya29.fake")
		return 0
	}
	fmt.Fprintf(os.Stderr, "fake gcloud: unexpected arguments %q\n", args)
	return 2
}

// fakeWait waits for a line on stdin (or for it to close), standing in for
// a long-running program such as the xf TUI in a terminal.
func fakeWait([]string) int {
	_, _ = bufio.NewReader(os.Stdin).ReadString('\n')
	return 0
}

func envInt(key string) int {
	n, _ := strconv.Atoi(os.Getenv(key))
	return n
}

func envSeconds(key string) time.Duration {
	f, _ := strconv.ParseFloat(os.Getenv(key), 64)
	return time.Duration(f * float64(time.Second))
}
