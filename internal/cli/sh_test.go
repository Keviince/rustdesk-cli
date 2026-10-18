package cli

import (
	"bytes"
	"errors"
	"io"
	"strings"
	"testing"
	"testing/synctest"
	"time"
)

func TestShRejectsNegativeIdle(t *testing.T) {
	cmd := NewRootCmd()
	cmd.SetArgs([]string{"sh", "--idle=-1s"})
	cmd.SilenceUsage = true
	cmd.SilenceErrors = true
	var usage *UsageError
	if err := cmd.Execute(); !errors.As(err, &usage) || !strings.Contains(err.Error(), "--idle") {
		t.Fatalf("negative idle error = %v, want usage error for --idle", err)
	}
}

func TestShRejectsNegativeCommandTimeout(t *testing.T) {
	cmd := NewRootCmd()
	cmd.SetArgs([]string{"sh", "--command-timeout=-1s"})
	cmd.SilenceUsage = true
	cmd.SilenceErrors = true
	var usage *UsageError
	if err := cmd.Execute(); !errors.As(err, &usage) || !strings.Contains(err.Error(), "--command-timeout") {
		t.Fatalf("negative command timeout error = %v, want usage error", err)
	}
}

func TestShOneShotIdleDisabledPreservesDelayedOutput(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		reader, writer := io.Pipe()
		defer reader.Close()
		go func() {
			defer writer.Close()
			io.WriteString(writer, "Downloading...\n")
			time.Sleep(2 * time.Minute)
			io.WriteString(writer, "Installation complete\n")
		}()

		var output bytes.Buffer
		if err := copyShOneShotOutput(reader, &output, 0); err != nil {
			t.Fatal(err)
		}
		if got, want := output.String(), "Downloading...\nInstallation complete\n"; got != want {
			t.Fatalf("output = %q, want %q", got, want)
		}
	})
}

func TestShOneShotIdleHonorsMinimumWait(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		reader, writer := io.Pipe()
		defer reader.Close()
		defer writer.Close()
		go io.WriteString(writer, "done\n")

		start := time.Now()
		var output bytes.Buffer
		if err := copyShOneShotOutput(reader, &output, oneShotIdle); err != nil {
			t.Fatal(err)
		}
		if elapsed := time.Since(start); elapsed != oneShotMinWait {
			t.Fatalf("waited %v, want %v", elapsed, oneShotMinWait)
		}
		if output.String() != "done\n" {
			t.Fatalf("output = %q", output.String())
		}
	})
}

func TestShOneShotIdleRestartsAfterOutput(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		reader, writer := io.Pipe()
		defer reader.Close()
		defer writer.Close()
		go func() {
			io.WriteString(writer, "start\n")
			time.Sleep(900 * time.Millisecond)
			io.WriteString(writer, "progress\n")
			time.Sleep(200 * time.Millisecond)
			io.WriteString(writer, "done\n")
		}()

		start := time.Now()
		var output bytes.Buffer
		if err := copyShOneShotOutput(reader, &output, oneShotIdle); err != nil {
			t.Fatal(err)
		}
		if elapsed, want := time.Since(start), 1400*time.Millisecond; elapsed != want {
			t.Fatalf("waited %v, want %v", elapsed, want)
		}
		if got, want := output.String(), "start\nprogress\ndone\n"; got != want {
			t.Fatalf("output = %q, want %q", got, want)
		}
	})
}

func TestShOneShotWaitsForFirstOutput(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		reader, writer := io.Pipe()
		defer reader.Close()
		go func() {
			defer writer.Close()
			time.Sleep(2 * time.Second)
			io.WriteString(writer, "ready\n")
		}()

		var output bytes.Buffer
		if err := copyShOneShotOutput(reader, &output, oneShotIdle); err != nil {
			t.Fatal(err)
		}
		if output.String() != "ready\n" {
			t.Fatalf("output = %q, want delayed output", output.String())
		}
	})
}

type shReaderFunc func([]byte) (int, error)

func (f shReaderFunc) Read(p []byte) (int, error) { return f(p) }

func TestShInteractiveReportsReadFailure(t *testing.T) {
	readErr := errors.New("terminal read timed out")
	reader := shReaderFunc(func(p []byte) (int, error) {
		return copy(p, "Downloading...\n"), readErr
	})
	var output bytes.Buffer
	if err := copyShInteractiveOutput(reader, &output); !errors.Is(err, readErr) {
		t.Fatalf("error = %v, want read failure", err)
	}
	if output.String() != "Downloading...\n" {
		t.Fatalf("output = %q, want final data before failure", output.String())
	}
}

func TestShInteractiveNormalClose(t *testing.T) {
	var output bytes.Buffer
	if err := copyShInteractiveOutput(strings.NewReader("done\n"), &output); err != nil {
		t.Fatal(err)
	}
	if output.String() != "done\n" {
		t.Fatalf("output = %q", output.String())
	}
}

func TestShOneShotPreservesFinalOutputAndReadError(t *testing.T) {
	readErr := errors.New("remote connection lost")
	for _, endErr := range []error{io.EOF, readErr} {
		t.Run(endErr.Error(), func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				prefix := strings.Repeat("output\n", 1000)
				reader := io.MultiReader(strings.NewReader(prefix), shReaderFunc(func(p []byte) (int, error) {
					return copy(p, "final\n"), endErr
				}))
				var output bytes.Buffer
				err := copyShOneShotOutput(reader, &output, oneShotIdle)
				if endErr == io.EOF && err != nil {
					t.Fatal(err)
				}
				if endErr == readErr && !errors.Is(err, readErr) {
					t.Fatalf("error = %v, want %v", err, readErr)
				}
				if got, want := output.String(), prefix+"final\n"; got != want {
					t.Fatalf("output length = %d, want %d with final data", len(got), len(want))
				}
			})
		})
	}
}
