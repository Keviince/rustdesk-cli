package cli

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/base64"
	"encoding/binary"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"strconv"
	"strings"
	"time"
	"unicode/utf16"
)

type commandTerminal interface {
	io.Reader
	io.Writer
	Abort() error
}

func encodePowerShell(script string) string {
	units := utf16.Encode([]rune(script))
	data := make([]byte, 2*len(units))
	for i, unit := range units {
		binary.LittleEndian.PutUint16(data[2*i:], unit)
	}
	return base64.StdEncoding.EncodeToString(data)
}

func windowsCommandInput(command, marker string) string {
	command = "$ErrorActionPreference='Stop'\r\n$ProgressPreference='SilentlyContinue'\r\n[Console]::OutputEncoding=[System.Text.UTF8Encoding]::new($false)\r\n" + command
	// The user code runs in a separate process so even `exit` leaves the
	// supervisor alive to report its status. WaitForExit waits only for that
	// process; Start-Process -Wait would also wait for persistent UI children.
	script := fmt.Sprintf(`$ErrorActionPreference='Stop'
$ProgressPreference='SilentlyContinue'
[Console]::OutputEncoding=[System.Text.UTF8Encoding]::new($false)
%s
$rdcliCode=1
try {
  $rdcliInfo=[System.Diagnostics.ProcessStartInfo]::new()
  $rdcliInfo.FileName=Join-Path $PSHOME 'powershell.exe'
  $rdcliInfo.Arguments='-NoLogo -NoProfile -NonInteractive -OutputFormat Text -EncodedCommand %s'
  $rdcliInfo.UseShellExecute=$false
  $rdcliProcess=[System.Diagnostics.Process]::Start($rdcliInfo)
  $rdcliProcess.WaitForExit()
  $rdcliCode=$rdcliProcess.ExitCode
  $rdcliProcess.Dispose()
  if ($null -eq $rdcliCode) { throw 'command process returned no exit status' }
} catch {
  [Console]::Error.WriteLine($_.Exception.Message)
  $rdcliCode=1
}
`, "[Console]::Write(\"`r`n"+windowsCommandStart(marker)+"`r`n\")", encodePowerShell(command))
	// Start the record on its own console line without inserting a blank line
	// after commands that already printed a newline.
	script += "if (-not [Console]::IsOutputRedirected -and [Console]::CursorLeft -ne 0) { [Console]::Write(\"`r`n\") }\n"
	script += "[Console]::Write(\"" + marker + "\"+[string]$rdcliCode+\"__`r`n\")\n"
	// Only Base64 is echoed by the interactive shell, never a completion marker.
	return "powershell.exe -NoLogo -NoProfile -NonInteractive -OutputFormat Text -EncodedCommand " + encodePowerShell(script) + "\r"
}

func windowsCommandStart(marker string) string {
	return strings.TrimSuffix(strings.Replace(marker, "__RDCLI_DONE_", "__RDCLI_START_", 1), ":")
}

func runWindowsCommand(ctx context.Context, terminal commandTerminal, output io.Writer, command string, timeout time.Duration) error {
	var nonce [16]byte
	if _, err := rand.Read(nonce[:]); err != nil {
		return fmt.Errorf("generate command completion identifier: %w", err)
	}
	marker := "__RDCLI_DONE_" + hex.EncodeToString(nonce[:]) + "__:"
	return executeWindowsCommand(ctx, terminal, output, windowsCommandInput(command, marker), marker, timeout)
}

func executeWindowsCommand(ctx context.Context, terminal commandTerminal, output io.Writer, input, marker string, timeout time.Duration) error {
	if timeout > 0 {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, timeout)
		defer cancel()
	}
	if err := ctx.Err(); err != nil {
		return commandContextError(err)
	}
	aborted := make(chan struct{})
	stopAbort := context.AfterFunc(ctx, func() {
		defer close(aborted)
		_ = terminal.Abort()
	})
	defer func() {
		if !stopAbort() {
			<-aborted
		}
	}()

	n, err := io.WriteString(terminal, input)
	if err == nil && n != len(input) {
		err = io.ErrShortWrite
	}
	if err == nil {
		err = copyWindowsCommandResult(terminal, output, marker)
	} else {
		err = fmt.Errorf("submit remote command: %w", err)
	}
	if err != nil && ctx.Err() != nil {
		return commandContextError(ctx.Err())
	}
	return err
}

func copyWindowsCommandResult(terminal io.Reader, output io.Writer, marker string) error {
	terminal = &commandTextReader{source: terminal}
	start := []byte(windowsCommandStart(marker) + "\n")
	pending := make([]byte, 0, 4096+len(start))
	buf := make([]byte, 4096)
	for {
		n, err := terminal.Read(buf)
		pending = append(pending, buf[:n]...)
		if at := bytes.Index(pending, start); at >= 0 {
			// Discard the startup banner and PSReadLine redraws of the encoded
			// invocation. The nonce is only visible once the supervisor executes.
			var tail io.Reader = terminal
			if err != nil {
				tail = commandReadFailure{err: err}
			}
			reader := io.MultiReader(bytes.NewReader(pending[at+len(start):]), tail)
			return copyWindowsCommandOutput(reader, output, marker)
		}
		if err != nil {
			return fmt.Errorf("remote command did not start (no start record): %w", err)
		}
		if len(pending) >= len(start) {
			pending = append(pending[:0], pending[len(pending)-len(start)+1:]...)
		}
	}
}

type commandReadFailure struct{ err error }

func (r commandReadFailure) Read([]byte) (int, error) { return 0, r.err }

func commandContextError(err error) error {
	if errors.Is(err, context.DeadlineExceeded) {
		return fmt.Errorf("remote command timed out (--command-timeout): %w", err)
	}
	return fmt.Errorf("remote command canceled: %w", err)
}

type commandOutput struct {
	output   io.Writer
	marker   []byte
	pending  []byte
	complete bool
	exitCode int32
}

func (s *commandOutput) writeOutput(data []byte) error {
	if len(data) == 0 {
		return nil
	}
	n, err := s.output.Write(data)
	if err != nil {
		return fmt.Errorf("write terminal output: %w", err)
	}
	if n != len(data) {
		return fmt.Errorf("write terminal output: %w", io.ErrShortWrite)
	}
	return nil
}

func (s *commandOutput) consume(data []byte) error {
	s.pending = append(s.pending, data...)
	if start := bytes.Index(s.pending, s.marker); start >= 0 {
		if err := s.writeOutput(s.pending[:start]); err != nil {
			return err
		}
		s.pending = s.pending[start:]
		status := s.pending[len(s.marker):]
		if end := bytes.Index(status, []byte("__")); end >= 0 {
			code, err := strconv.ParseInt(string(status[:end]), 10, 32)
			if err != nil || strings.TrimSpace(string(status[:end])) != string(status[:end]) {
				return errors.New("invalid remote command completion status")
			}
			s.exitCode = int32(code)
			s.complete = true
			s.pending = nil
			return nil
		}
		// An int32 status and a partial terminator need at most 12 bytes.
		if len(status) > 12 {
			return errors.New("invalid remote command completion status")
		}
		return nil
	}

	// Retain only a possible marker prefix, streaming even newline-free output.
	keep := min(len(s.pending), len(s.marker)-1)
	for keep > 0 && !bytes.Equal(s.pending[len(s.pending)-keep:], s.marker[:keep]) {
		keep--
	}
	end := len(s.pending) - keep
	if err := s.writeOutput(s.pending[:end]); err != nil {
		return err
	}
	s.pending = s.pending[end:]
	return nil
}

func copyWindowsCommandOutput(terminal io.Reader, output io.Writer, marker string) error {
	stream := commandOutput{output: output, marker: []byte(marker)}
	buf := make([]byte, 4096)
	for {
		n, err := terminal.Read(buf)
		if consumeErr := stream.consume(buf[:n]); consumeErr != nil {
			return consumeErr
		}
		if stream.complete {
			if stream.exitCode != 0 {
				return fmt.Errorf("remote command failed (exit code %d)", stream.exitCode)
			}
			return nil
		}
		if err != nil {
			if outputErr := stream.writeOutput(stream.pending); outputErr != nil {
				return outputErr
			}
			if errors.Is(err, io.EOF) {
				return errors.New("remote terminal closed before command completion; result unknown")
			}
			return fmt.Errorf("read remote terminal before command completion: %w", err)
		}
	}
}
