package cli

import (
	"bytes"
	"errors"
	"io"
	"strings"
	"testing"
)

func TestWindowsCommandOutputFormatting(t *testing.T) {
	for _, tc := range []struct{ name, body, want string }{
		{"silent", "", ""},
		{"normal lines", "first\r\nsecond\r\n", "first\nsecond\n"},
		{"unfinished line", "last output", "last output"},
		{"intentional blanks", "\r\nfirst\r\n\r\nsecond\r\n\r\n", "\nfirst\n\nsecond\n\n"},
		{"cursor and color", "\x1b[?25l\x1b[2;1H\x1b[32m中文 🐦\x1b[m\x1b[?25h\r\n", "中文 🐦\n"},
		{"clear screen", "\x1b[H" + strings.Repeat("\x1b[K\r\n", 24) + "\x1b[Hdone\r\n", "done\n"},
		{"title and hyperlinks", "\x1b]0;remote title\a\x1b]8;;https://example.com\x1b\\text\x1b]8;;\x1b\\\n", "text\n"},
		{"carriage returns", "10%\r50%\r100%\r\n", "10%\n50%\n100%\n"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			data := "banner\r\n" + windowsCommandStart(testCommandMarker) + "\r\n" + tc.body + testCommandMarker + "0__\r\nPS C:\\> "
			for split := 0; split <= len(data); split++ {
				var output bytes.Buffer
				reader := io.MultiReader(strings.NewReader(data[:split]), strings.NewReader(data[split:]))
				if err := copyWindowsCommandResult(reader, &output, testCommandMarker); err != nil {
					t.Fatalf("split %d: %v", split, err)
				}
				if output.String() != tc.want {
					t.Fatalf("split %d: output=%q, want %q", split, output.String(), tc.want)
				}
			}
			var output bytes.Buffer
			reader := &shByteReader{data: []byte(data)}
			if err := copyWindowsCommandResult(reader, &output, testCommandMarker); err != nil || output.String() != tc.want {
				t.Fatalf("bytewise decode: error=%v output=%q, want %q", err, output.String(), tc.want)
			}
		})
	}
}

type shByteReader struct{ data []byte }

func (r *shByteReader) Read(p []byte) (int, error) {
	if len(r.data) == 0 {
		return 0, io.EOF
	}
	n := copy(p, r.data[:1])
	r.data = r.data[n:]
	return n, nil
}

func TestCommandTextReaderPreservesReadError(t *testing.T) {
	readErr := errors.New("lost connection")
	calls := 0
	r := &commandTextReader{source: shReaderFunc(func(p []byte) (int, error) {
		calls++
		return copy(p, "last\r\n"), readErr
	})}
	data, err := io.ReadAll(r)
	if !errors.Is(err, readErr) || string(data) != "last\n" || calls != 1 {
		t.Fatalf("data=%q error=%v calls=%d", data, err, calls)
	}
}
