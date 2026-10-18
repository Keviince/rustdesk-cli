package main

import (
	"errors"
	"fmt"
	"io"
	"os"

	"rdcli/internal/cli"
)

func run(args []string, stdout, stderr io.Writer) int {
	cmd := cli.NewRootCmd()
	cmd.SilenceUsage = true
	cmd.SilenceErrors = true
	cmd.SetOut(stdout)
	cmd.SetErr(stderr)
	if _, _, err := cmd.Find(args); err != nil {
		fmt.Fprintln(stderr, err)
		return 2
	}
	cmd.SetArgs(args)
	if err := cmd.Execute(); err != nil {
		fmt.Fprintln(stderr, err)
		var usage *cli.UsageError
		if errors.As(err, &usage) {
			return 2
		}
		return 1
	}
	return 0
}

func main() {
	os.Exit(run(os.Args[1:], os.Stdout, os.Stderr))
}
