package cli

import (
	"errors"
	"fmt"
	"io"
	"os"
	"os/signal"
	"strings"
	"time"

	"rdcli/internal/hbb"

	"github.com/spf13/cobra"
	"golang.org/x/term"

	pb "rdcli/internal/hbb/proto"
)

const (
	oneShotMinWait = 1 * time.Second
	oneShotIdle    = 300 * time.Millisecond
)

func newShCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "sh [<ref>] [command]",
		Short: "remote shell session or command execution",
		RunE:  runSh,
	}
	cmd.Flags().Duration("idle", oneShotIdle, "one-shot idle timeout after output (0 disables; wait for remote shell to close)")
	cmd.Flags().Duration("command-timeout", 30*time.Minute, "Windows command execution timeout (0 disables; ignored with --idle)")
	return cmd
}

func runSh(cmd *cobra.Command, args []string) error {
	idle, _ := cmd.Flags().GetDuration("idle")
	if idle < 0 {
		return usageErrorf("--idle must be nonnegative (0 disables the idle timeout)")
	}
	commandTimeout, _ := cmd.Flags().GetDuration("command-timeout")
	if commandTimeout < 0 {
		return usageErrorf("--command-timeout must be nonnegative (0 disables the execution timeout)")
	}

	peerID, password, remArgs, err := resolveTarget(cmd, args)
	if err != nil {
		return err
	}
	opts := dialOpts(cmd, peerID, password, pb.ConnType_TERMINAL)

	if len(remArgs) > 0 {
		command := strings.Join(remArgs, " ")
		return runShOneShot(cmd, opts, command, idle, commandTimeout)
	}

	return runShInteractive(opts)
}

func runShInteractive(opts hbb.DialOpts) error {
	stdoutFd := int(os.Stdout.Fd())
	cols, rows, err := term.GetSize(stdoutFd)
	if err != nil || cols <= 0 || rows <= 0 {
		cols = 80
		rows = 24
	}

	stdinFd := int(os.Stdin.Fd())
	oldState, err := term.MakeRaw(stdinFd)
	if err == nil {
		defer term.Restore(stdinFd, oldState)
	}

	ts, err := hbb.OpenTerminal(opts, uint32(rows), uint32(cols))
	if err != nil {
		return err
	}
	defer ts.Close()

	sigCh := make(chan os.Signal, 1)
	notifySigwinch(sigCh)
	defer signal.Stop(sigCh)

	go func() {
		for range sigCh {
			if c, r, err := term.GetSize(stdoutFd); err == nil && c > 0 && r > 0 {
				_ = ts.Resize(uint32(r), uint32(c))
			}
		}
	}()

	go func() {
		buf := make([]byte, 1024)
		for {
			n, err := os.Stdin.Read(buf)
			if n > 0 {
				if _, werr := ts.Write(buf[:n]); werr != nil {
					return
				}
			}
			if err != nil {
				return
			}
		}
	}()

	return copyShInteractiveOutput(ts, os.Stdout)
}

func copyShInteractiveOutput(terminal io.Reader, output io.Writer) error {
	if _, err := io.Copy(output, terminal); err != nil {
		return fmt.Errorf("remote terminal: %w", err)
	}
	return nil
}

func runShOneShot(cmd *cobra.Command, opts hbb.DialOpts, command string, idle, commandTimeout time.Duration) error {
	cols, rows, err := term.GetSize(int(os.Stdout.Fd()))
	if err != nil || cols <= 0 || rows <= 0 {
		cols = 80
		rows = 24
	}
	// Keep completion records on one line even in a narrow local terminal.
	if cols < 80 {
		cols = 80
	}

	ts, err := hbb.OpenTerminal(opts, uint32(rows), uint32(cols))
	if err != nil {
		return err
	}
	defer ts.Close()
	if strings.EqualFold(ts.PeerInfo().GetPlatform(), "Windows") && !cmd.Flags().Changed("idle") {
		ctx, stop := signal.NotifyContext(cmd.Context(), os.Interrupt)
		defer stop()
		return runWindowsCommand(ctx, ts, cmd.OutOrStdout(), command, commandTimeout)
	}

	if _, err := ts.Write([]byte(command + "\r")); err != nil {
		return err
	}

	return copyShOneShotOutput(ts, cmd.OutOrStdout(), idle)
}

// The caller must close the terminal after this returns to unblock a pending Read.
func copyShOneShotOutput(terminal io.Reader, output io.Writer, idle time.Duration) error {
	type readResult struct {
		data []byte
		err  error
	}
	results := make(chan readResult, 1)
	done := make(chan struct{})
	defer close(done)

	go func() {
		for {
			buf := make([]byte, 4096)
			n, err := terminal.Read(buf)
			if n > 0 || err != nil {
				select {
				case results <- readResult{data: buf[:n], err: err}:
				case <-done:
					return
				}
			}
			if err != nil {
				return
			}
		}
	}()

	start := time.Now()
	var idleC <-chan time.Time
	var idleTimer *time.Timer
	defer func() {
		if idleTimer != nil {
			idleTimer.Stop()
		}
	}()
	armIdle := func() {
		if idle == 0 {
			return
		}
		wait := idle
		if remaining := oneShotMinWait - time.Since(start); remaining > wait {
			wait = remaining
		}
		if idleTimer == nil {
			idleTimer = time.NewTimer(wait)
		} else {
			if !idleTimer.Stop() {
				select {
				case <-idleTimer.C:
				default:
				}
			}
			idleTimer.Reset(wait)
		}
		idleC = idleTimer.C
	}

	for {
		select {
		case result := <-results:
			if len(result.data) > 0 {
				n, err := output.Write(result.data)
				if err != nil {
					return fmt.Errorf("write terminal output: %w", err)
				}
				if n != len(result.data) {
					return fmt.Errorf("write terminal output: %w", io.ErrShortWrite)
				}
				armIdle()
			}
			if result.err != nil {
				if !errors.Is(result.err, io.EOF) {
					return fmt.Errorf("read remote terminal: %w", result.err)
				}
				return nil
			}
		case <-idleC:
			return nil
		}
	}
}
