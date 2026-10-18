package cli

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/binary"
	"errors"
	"io"
	"os/exec"
	"runtime"
	"strings"
	"testing"
	"testing/synctest"
	"time"
	"unicode/utf16"
)

const testCommandMarker = "__RDCLI_DONE_0123456789abcdef0123456789abcdef__:"

func TestWindowsCommandSuppressesStartupEcho(t *testing.T) {
	start := windowsCommandStart(testCommandMarker)
	echo := strings.Repeat("PS C:\\> powershell.exe -EncodedCommand ABCDEFGH\r\n", 200)
	want := "user output\n"
	for split := 0; split <= len(start); split++ {
		reader := io.MultiReader(strings.NewReader(echo+start[:split]), strings.NewReader(start[split:]+"\r\nuser output\r\n"+testCommandMarker+"0__\r\n"))
		var output bytes.Buffer
		if err := copyWindowsCommandResult(reader, &output, testCommandMarker); err != nil {
			t.Fatalf("split %d: %v", split, err)
		}
		if output.String() != want {
			t.Fatalf("split %d: leaked startup output: %q", split, output.String())
		}
	}
}

func TestWindowsCommandStartReadFailures(t *testing.T) {
	readErr := errors.New("connection lost with buffered output")
	for _, prefix := range []string{"before start", windowsCommandStart(testCommandMarker) + "\r\nfinal output"} {
		calls := 0
		reader := shReaderFunc(func(p []byte) (int, error) {
			calls++
			return copy(p, prefix), readErr
		})
		var output bytes.Buffer
		if err := copyWindowsCommandResult(reader, &output, testCommandMarker); !errors.Is(err, readErr) || calls != 1 {
			t.Fatalf("read error=%v calls=%d, want original failure without another read", err, calls)
		}
		if strings.HasSuffix(prefix, "final output") && output.String() != "final output" {
			t.Fatalf("lost command output: %q", output.String())
		}
	}
	var output bytes.Buffer
	if err := copyWindowsCommandResult(strings.NewReader("banner only"), &output, testCommandMarker); err == nil || !strings.Contains(err.Error(), "did not start") {
		t.Fatalf("missing start error=%v", err)
	}
}

func TestWindowsCommandCompletionAtEveryBoundary(t *testing.T) {
	before := "banner\r\n\x1b[32m安装完成 🐦\x1b[0m\r\n"
	data := before + testCommandMarker + "0__\r\nPS C:\\> "
	for split := 0; split <= len(data); split++ {
		var output bytes.Buffer
		reader := io.MultiReader(strings.NewReader(data[:split]), strings.NewReader(data[split:]))
		if err := copyWindowsCommandOutput(reader, &output, testCommandMarker); err != nil {
			t.Fatalf("split %d: %v", split, err)
		}
		if output.String() != before {
			t.Fatalf("split %d: output = %q, want %q", split, output.String(), before)
		}
	}
	var output bytes.Buffer
	stream := &commandOutput{output: &output, marker: []byte(testCommandMarker)}
	for _, b := range []byte(data) {
		if stream.complete {
			break
		}
		if err := stream.consume([]byte{b}); err != nil {
			t.Fatal(err)
		}
	}
	if !stream.complete || output.String() != before {
		t.Fatalf("bytewise decode: complete=%t output=%q", stream.complete, output.String())
	}
}

func TestWindowsCommandCompletionResults(t *testing.T) {
	for _, tc := range []struct{ name, data, want, output string }{
		{"silent success", testCommandMarker + "0__\r\n", "", ""},
		{"failure", "failed\n" + testCommandMarker + "7__\n", "exit code 7", "failed\n"},
		{"negative status", testCommandMarker + "-1__\n", "exit code -1", ""},
		{"terminal closed", "last output", "result unknown", "last output"},
		{"wrong identifier", "__RDCLI_DONE_other__:0__\n", "result unknown", "__RDCLI_DONE_other__:0__\n"},
		{"incomplete marker", "out\n" + testCommandMarker[:19], "result unknown", "out\n" + testCommandMarker[:19]},
		{"invalid status", testCommandMarker + "oops__\n", "invalid remote command completion status", ""},
		{"oversized status", testCommandMarker + "12345678901234567890", "invalid remote command completion status", ""},
		{"out of range", testCommandMarker + "2147483648__", "invalid remote command completion status", ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var output bytes.Buffer
			err := copyWindowsCommandOutput(strings.NewReader(tc.data), &output, testCommandMarker)
			if tc.want == "" && err != nil || tc.want != "" && (err == nil || !strings.Contains(err.Error(), tc.want)) {
				t.Fatalf("error = %v, want %q", err, tc.want)
			}
			if output.String() != tc.output {
				t.Fatalf("output = %q, want %q", output.String(), tc.output)
			}
		})
	}
}

func TestWindowsCommandStreamsWithoutNewlines(t *testing.T) {
	var output bytes.Buffer
	stream := &commandOutput{output: &output, marker: []byte(testCommandMarker)}
	chunk := bytes.Repeat([]byte("x"), 4096)
	for i := 0; i < 100; i++ {
		if err := stream.consume(chunk); err != nil {
			t.Fatal(err)
		}
		if output.Len() != (i+1)*len(chunk) || len(stream.pending) != 0 {
			t.Fatal("newline-free output was buffered instead of streamed")
		}
	}
}

type commandWriterFunc func([]byte) (int, error)

func (f commandWriterFunc) Write(p []byte) (int, error) { return f(p) }

func TestWindowsCommandIOFailures(t *testing.T) {
	readErr := errors.New("connection lost")
	reader := shReaderFunc(func(p []byte) (int, error) { return copy(p, "final output"), readErr })
	var output bytes.Buffer
	if err := copyWindowsCommandOutput(reader, &output, testCommandMarker); !errors.Is(err, readErr) {
		t.Fatalf("error = %v, want read failure", err)
	}
	if output.String() != "final output" {
		t.Fatalf("lost final output: %q", output.String())
	}
	writeErr := errors.New("output unavailable")
	for _, tc := range []struct {
		writer io.Writer
		want   error
	}{
		{commandWriterFunc(func([]byte) (int, error) { return 0, writeErr }), writeErr},
		{commandWriterFunc(func([]byte) (int, error) { return 0, nil }), io.ErrShortWrite},
	} {
		if err := copyWindowsCommandOutput(strings.NewReader("output"), tc.writer, testCommandMarker); !errors.Is(err, tc.want) {
			t.Fatalf("error = %v, want %v", err, tc.want)
		}
	}
}

type fakeCommandTerminal struct {
	reader io.Reader
	write  func([]byte) (int, error)
	abort  func() error
}

func (f *fakeCommandTerminal) Read(p []byte) (int, error)  { return f.reader.Read(p) }
func (f *fakeCommandTerminal) Write(p []byte) (int, error) { return f.write(p) }
func (f *fakeCommandTerminal) Abort() error                { return f.abort() }

func TestWindowsCommandWaitsThroughSilence(t *testing.T) {
	for _, timeout := range []time.Duration{0, 30 * time.Minute} {
		synctest.Test(t, func(t *testing.T) {
			reader, writer := io.Pipe()
			defer reader.Close()
			defer writer.Close()
			terminal := &fakeCommandTerminal{reader: reader, abort: reader.Close}
			terminal.write = func(p []byte) (int, error) {
				go func() {
					io.WriteString(writer, windowsCommandStart(testCommandMarker)+"\r\n")
					io.WriteString(writer, "Downloading...\n")
					time.Sleep(2 * time.Minute)
					io.WriteString(writer, "Installed\r\n"+testCommandMarker+"0__\r\n")
				}()
				return len(p), nil
			}
			var output bytes.Buffer
			start := time.Now()
			if err := executeWindowsCommand(context.Background(), terminal, &output, "command\r", testCommandMarker, timeout); err != nil {
				t.Fatal(err)
			}
			if time.Since(start) != 2*time.Minute || output.String() != "Downloading...\nInstalled\n" {
				t.Fatalf("did not wait for completion: elapsed=%v output=%q", time.Since(start), output.String())
			}
		})
	}
}

func TestWindowsCommandTimeoutAndCancellation(t *testing.T) {
	for _, blocked := range []string{"read", "write"} {
		for _, cancel := range []bool{false, true} {
			t.Run(blocked+" cancel="+map[bool]string{false: "false", true: "true"}[cancel], func(t *testing.T) {
				synctest.Test(t, func(t *testing.T) {
					reader, writer := io.Pipe()
					defer reader.Close()
					defer writer.Close()
					aborted := false
					terminal := &fakeCommandTerminal{reader: reader, abort: func() error { aborted = true; return reader.Close() }}
					terminal.write = func(p []byte) (int, error) {
						if blocked == "write" {
							return writer.Write(p)
						}
						return len(p), nil
					}
					ctx, stop := context.WithCancel(context.Background())
					defer stop()
					want := context.DeadlineExceeded
					if cancel {
						want = context.Canceled
						go func() { time.Sleep(time.Second); stop() }()
					}
					err := executeWindowsCommand(ctx, terminal, io.Discard, "command\r", testCommandMarker, 2*time.Second)
					if !errors.Is(err, want) || !aborted {
						t.Fatalf("error=%v aborted=%t, want %v", err, aborted, want)
					}
				})
			})
		}
	}
}

func decodePowerShell(t *testing.T, encoded string) string {
	t.Helper()
	data, err := base64.StdEncoding.DecodeString(encoded)
	if err != nil || len(data)%2 != 0 {
		t.Fatalf("invalid PowerShell encoding: %v", err)
	}
	units := make([]uint16, len(data)/2)
	for i := range units {
		units[i] = binary.LittleEndian.Uint16(data[2*i:])
	}
	return string(utf16.Decode(units))
}

func TestWindowsCommandEncoding(t *testing.T) {
	command := `$x="中文 🐦 'quote'"; $x | Write-Output; exit 7`
	input := windowsCommandInput(command, testCommandMarker)
	if !strings.HasSuffix(input, "\r") || strings.Contains(input, testCommandMarker) {
		t.Fatal("input must submit Enter without echoing a completion marker")
	}
	fields := strings.Fields(input)
	wrapper := decodePowerShell(t, fields[len(fields)-1])
	if !strings.Contains(wrapper, testCommandMarker) || !strings.Contains(wrapper, ".WaitForExit()") || strings.Contains(wrapper, " -Wait ") {
		t.Fatal("supervisor must report status after waiting only for the command process")
	}
	start := strings.Index(wrapper, "-EncodedCommand ")
	if start < 0 {
		t.Fatal("missing encoded child command")
	}
	start += len("-EncodedCommand ")
	end := strings.Index(wrapper[start:], "'")
	child := decodePowerShell(t, wrapper[start:start+end])
	if !strings.HasSuffix(child, command) {
		t.Fatalf("command changed across encoding: %q", child)
	}
}

func TestWindowsCommandNativePowerShell(t *testing.T) {
	if runtime.GOOS != "windows" {
		t.Skip("requires Windows PowerShell")
	}
	powershell, err := exec.LookPath("powershell.exe")
	if err != nil {
		t.Skip("Windows PowerShell not installed")
	}
	for _, tc := range []struct{ name, command, output, want string }{
		{"unicode and pipeline", `$s="中文 🐦 'quoted'"; $s | ForEach-Object { Write-Output $_ }; Write-Host 'host-log'`, "中文 🐦 'quoted'", ""},
		{"silent success", "exit 0", "", ""},
		{"unfinished line", "[Console]::Write('unfinished')", "unfinished", ""},
		{"explicit exit", "Write-Output 'before-exit'; exit 7", "before-exit", "exit code 7"},
		{"exception", "throw 'probe exception'", "probe exception", "exit code 1"},
		{"input prompt", "Read-Host 'probe prompt'", "NonInteractive", "exit code 1"},
		{"syntax error", "if (", "", "exit code 1"},
		{"native failure", "cmd.exe /c exit 9", "", "exit code 1"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
			defer cancel()
			args := strings.Fields(windowsCommandInput(tc.command, testCommandMarker))[1:]
			cmd := exec.CommandContext(ctx, powershell, args...)
			var stdout, stderr bytes.Buffer
			cmd.Stdout, cmd.Stderr = &stdout, &stderr
			if err := cmd.Run(); err != nil {
				t.Fatalf("supervisor failed: %v; stdout=%s stderr=%s", err, &stdout, &stderr)
			}
			var output bytes.Buffer
			err := copyWindowsCommandResult(&stdout, &output, testCommandMarker)
			if tc.want == "" && err != nil || tc.want != "" && (err == nil || !strings.Contains(err.Error(), tc.want)) {
				t.Fatalf("error=%v, want %q; stdout=%s stderr=%s", err, tc.want, &output, &stderr)
			}
			if tc.output != "" && !strings.Contains(output.String()+stderr.String(), tc.output) {
				t.Fatalf("missing %q: stdout=%s stderr=%s", tc.output, &output, &stderr)
			}
			if tc.name == "silent success" && output.Len() != 0 {
				t.Fatalf("silent command emitted framing whitespace: %q", output.String())
			}
		})
	}
}
