package cli

import (
	"bytes"
	"context"
	"io"
	"os"
	"strings"
	"testing"
	"time"
	"unsafe"

	"golang.org/x/sys/windows"
)

// Exercise the same console transport as RustDesk, including terminal echo and
// VT output, rather than only redirected PowerShell stdout.
func TestWindowsCommandConPTY(t *testing.T) {
	var inputRead, inputWrite, outputRead, outputWrite windows.Handle
	if err := windows.CreatePipe(&inputRead, &inputWrite, nil, 0); err != nil {
		t.Fatal(err)
	}
	defer windows.CloseHandle(inputRead)
	input := os.NewFile(uintptr(inputWrite), "conpty-input")
	defer input.Close()
	if err := windows.CreatePipe(&outputRead, &outputWrite, nil, 0); err != nil {
		t.Fatal(err)
	}
	output := os.NewFile(uintptr(outputRead), "conpty-output")
	defer output.Close()
	var console windows.Handle
	if err := windows.CreatePseudoConsole(windows.Coord{X: 80, Y: 24}, inputRead, outputWrite, 0, &console); err != nil {
		windows.CloseHandle(outputWrite)
		t.Skipf("ConPTY not available: %v", err)
	}
	defer func() {
		// Closing a pseudoconsole can emit final output; drain it during cleanup.
		drained := make(chan struct{})
		go func() { io.Copy(io.Discard, output); close(drained) }()
		windows.ClosePseudoConsole(console)
		windows.CloseHandle(outputWrite)
		<-drained
	}()
	attributes, err := windows.NewProcThreadAttributeList(1)
	if err != nil {
		t.Fatal(err)
	}
	defer attributes.Delete()
	// This attribute takes the HPCON value itself, not the address of the handle.
	update := windows.NewLazySystemDLL("kernel32.dll").NewProc("UpdateProcThreadAttribute")
	ok, _, callErr := update.Call(uintptr(unsafe.Pointer(attributes.List())), 0,
		windows.PROC_THREAD_ATTRIBUTE_PSEUDOCONSOLE, uintptr(console), unsafe.Sizeof(console), 0, 0)
	if ok == 0 {
		t.Fatal(callErr)
	}
	startup := windows.StartupInfoEx{ProcThreadAttributeList: attributes.List()}
	startup.Cb = uint32(unsafe.Sizeof(startup))
	// Null standard handles prevent inheriting go test's redirected pipes.
	startup.Flags = windows.STARTF_USESTDHANDLES
	commandLine, err := windows.UTF16PtrFromString("powershell.exe -NoLogo -NoProfile")
	if err != nil {
		t.Fatal(err)
	}
	var process windows.ProcessInformation
	if err := windows.CreateProcess(nil, commandLine, nil, nil, false, windows.EXTENDED_STARTUPINFO_PRESENT,
		nil, nil, &startup.StartupInfo, &process); err != nil {
		t.Fatal(err)
	}
	defer windows.CloseHandle(process.Process)
	defer windows.CloseHandle(process.Thread)
	defer windows.TerminateProcess(process.Process, 1)
	terminal := &fakeCommandTerminal{
		reader: output,
		write:  input.Write,
		abort: func() error {
			windows.TerminateProcess(process.Process, 1)
			return output.Close()
		},
	}
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	var result bytes.Buffer
	err = runWindowsCommand(ctx, terminal, &result, "Clear-Host; Write-Host 'conpty-probe'; Write-Progress -Activity 'probe download' -PercentComplete 50; Start-Sleep -Milliseconds 350; Write-Progress -Activity 'probe download' -Completed; exit 7", 15*time.Second)
	if err == nil || !strings.Contains(err.Error(), "exit code 7") || result.String() != "conpty-probe\n" {
		t.Fatalf("ConPTY command: %v; output tail=%q", err, result.String()[max(0, result.Len()-1000):])
	}
	for _, length := range []int{0, 30, 70} {
		result.Reset()
		command := "[Console]::Write('" + strings.Repeat("x", length) + "')"
		err := runWindowsCommand(ctx, terminal, &result, command, 10*time.Second)
		want := strings.Repeat("x", length)
		if length > 0 {
			want += "\n"
		}
		if err != nil || result.String() != want {
			t.Fatalf("ConPTY length=%d: %v; output=%q", length, err, result.String())
		}
	}
}
