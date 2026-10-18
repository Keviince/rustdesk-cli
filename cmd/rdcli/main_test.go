package main

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestRun(t *testing.T) {
	t.Setenv("RDC_CONFIG", filepath.Join(t.TempDir(), "missing.toml"))
	for _, tc := range []struct {
		args   []string
		code   int
		output string
	}{
		{[]string{"version"}, 0, "rdcli v0.1.3"},
		{[]string{"auth", "login", "--help"}, 0, "--password-stdin"},
		{[]string{"auth", "login", "extra"}, 2, "usage:"},
		{[]string{"--help"}, 0, "--relay-server"},
		{[]string{"unknown-command"}, 2, "unknown command"},
		{[]string{"--unknown-flag"}, 2, "unknown flag"},
		{[]string{"--timeout=bad", "sh"}, 2, "invalid argument"},
		{[]string{"sh", "--command-timeout=-1s"}, 2, "--command-timeout"},
		{[]string{"sh", "--help"}, 0, "--command-timeout"},
		{[]string{"auth", "set"}, 2, "usage:"},
		{[]string{"sh"}, 2, "no machine specified"},
	} {
		t.Run(strings.Join(tc.args, " "), func(t *testing.T) {
			var out, errOut bytes.Buffer
			if code := run(tc.args, &out, &errOut); code != tc.code {
				t.Fatalf("code %d, want %d; stdout=%s stderr=%s", code, tc.code, &out, &errOut)
			}
			if !strings.Contains(out.String()+errOut.String(), tc.output) {
				t.Fatalf("expected %q; stdout=%s stderr=%s", tc.output, &out, &errOut)
			}
			if tc.code == 0 && errOut.Len() != 0 || tc.code != 0 && out.Len() != 0 {
				t.Fatalf("wrong output stream: stdout=%s stderr=%s", &out, &errOut)
			}
		})
	}
}

func TestRunConfigError(t *testing.T) {
	path := filepath.Join(t.TempDir(), "bad.toml")
	if err := os.WriteFile(path, []byte("[tags]\n\"peer\" = broken\n"), 0600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("RDC_CONFIG", path)
	var out, errOut bytes.Buffer
	if code := run([]string{"sh", "test-peer"}, &out, &errOut); code != 1 {
		t.Fatalf("code %d, want 1: %s", code, &errOut)
	}
	if out.Len() != 0 || !strings.Contains(errOut.String(), "config:") {
		t.Fatalf("stdout=%s stderr=%s", &out, &errOut)
	}
}
