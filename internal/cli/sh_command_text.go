package cli

import "io"

// A one-shot command shares the peer's console, but must not move the local
// cursor or erase earlier batch logs. Decode its VT transport as plain text
// before looking for protocol records, which can also contain VT sequences.
type commandTextReader struct {
	source      io.Reader
	pending     []byte
	err         error
	state       commandTextState
	carriage    bool
	lineContent bool
	erasedBlank bool
}

type commandTextState uint8

const (
	commandText commandTextState = iota
	commandEscape
	commandCSI
	commandString
	commandStringEscape
	commandCharset
)

func (r *commandTextReader) Read(p []byte) (int, error) {
	if len(p) == 0 {
		return 0, nil
	}
	for len(r.pending) == 0 && r.err == nil {
		var buf [4096]byte
		n, err := r.source.Read(buf[:])
		for _, b := range buf[:n] {
			r.consume(b)
		}
		r.err = err
		if err != nil && r.carriage {
			r.newline()
			r.carriage = false
		}
	}
	n := copy(p, r.pending)
	r.pending = r.pending[n:]
	if n > 0 {
		return n, nil
	}
	return 0, r.err
}

func (r *commandTextReader) newline() {
	// ConPTY clears a screen by emitting an erase followed by a newline for
	// every empty row. These are screen updates, not command output lines.
	if !r.erasedBlank || r.lineContent {
		r.pending = append(r.pending, '\n')
	}
	r.lineContent, r.erasedBlank = false, false
}

func (r *commandTextReader) consume(b byte) {
	switch r.state {
	case commandEscape:
		switch b {
		case '[':
			r.state = commandCSI
		case ']', 'P', '^', '_':
			r.state = commandString
		case '(', ')', '*', '+':
			r.state = commandCharset
		default:
			r.state = commandText
		}
		return
	case commandCSI:
		if b >= 0x40 && b <= 0x7e {
			if b == 'K' && !r.lineContent {
				r.erasedBlank = true
			}
			r.state = commandText
		}
		return
	case commandString:
		if b == '\a' {
			r.state = commandText
		} else if b == '\x1b' {
			r.state = commandStringEscape
		}
		return
	case commandStringEscape:
		if b == '\\' {
			r.state = commandText
		} else if b != '\x1b' {
			r.state = commandString
		}
		return
	case commandCharset:
		r.state = commandText
		return
	}
	if b == '\x1b' {
		r.state = commandEscape
		return
	}
	if r.carriage {
		r.newline()
		r.carriage = false
		if b == '\n' {
			return
		}
	}
	switch {
	case b == '\r':
		r.carriage = true
	case b == '\n':
		r.newline()
	case b == '\t' || b >= ' ' && b != 0x7f:
		r.pending = append(r.pending, b)
		r.lineContent = true
	}
}
