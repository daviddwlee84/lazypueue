// Package logview provides terminal-safe log ingestion and presentation without
// owning polling, transports, view focus, or clipboard access.
package logview

import (
	"strings"
	"unicode"
	"unicode/utf8"
)

type Buffer struct {
	maxLines, maxBytes  int
	lines               []string
	bytes               int
	styles              []lineStyle
	styleBytes          int
	currentStyle        lineStyle
	currentApplied      string
	active              sgrState
	activeCode          string
	csi                 []byte
	csiOverflow         bool
	plainOnly           bool
	current             []rune
	head, currentBytes  int
	pending             []byte
	state               byte
	carriage, truncated bool
}

func NewBuffer(maxLines, maxBytes int) *Buffer {
	if maxLines <= 0 {
		maxLines = 10000
	}
	if maxBytes <= 0 {
		maxBytes = 2 * 1024 * 1024
	}
	return &Buffer{maxLines: maxLines, maxBytes: maxBytes}
}
func (b *Buffer) defaults() {
	if b.maxLines <= 0 {
		b.maxLines = 10000
	}
	if b.maxBytes <= 0 {
		b.maxBytes = 2 * 1024 * 1024
	}
}
func (b *Buffer) Replace(text string) {
	lines, bytes := b.maxLines, b.maxBytes
	plainOnly := b.plainOnly
	*b = *NewBuffer(lines, bytes)
	b.plainOnly = plainOnly
	b.Append(text)
}
func (b *Buffer) Append(text string) {
	b.defaults()
	for i := 0; i < len(text); i++ {
		ch := text[i]
		switch b.state {
		case 'e': // Escape introducer: recognize control strings, CSI, and short escapes.
			switch ch {
			case '[':
				b.state = 'c'
				b.csi = b.csi[:0]
				b.csiOverflow = false
			case ']', 'P', 'X', '^', '_':
				b.state = 's'
			case 0x1b:
				b.state = 'e'
			default:
				if ch >= 0x20 && ch <= 0x2f {
					b.state = 'i'
				} else {
					b.state = 0
				}
			}
			continue
		case 'i':
			if ch >= 0x30 && ch <= 0x7e {
				b.state = 0
			}
			continue
		case 'c':
			if ch >= 0x40 && ch <= 0x7e {
				if ch == 'm' && !b.csiOverflow && !b.plainOnly {
					if state, ok := applySGR(b.active, b.csi); ok {
						b.active = state
						b.activeCode = state.sequence()
					}
				}
				b.state = 0
				b.csi = b.csi[:0]
			} else if ch == 0x1b {
				b.state = 'e'
				b.csi = b.csi[:0]
			} else if len(b.csi) < 128 {
				b.csi = append(b.csi, ch)
			} else {
				b.csiOverflow = true
			}
			continue
		case 's':
			if ch == 7 {
				b.state = 0
			} else if ch == 0x1b {
				b.state = 't'
			}
			continue
		case 't':
			if ch == '\\' {
				b.state = 0
			} else if ch != 0x1b {
				b.state = 's'
			}
			continue
		}
		b.pending = append(b.pending, ch)
		for len(b.pending) > 0 && utf8.FullRune(b.pending) {
			r, size := utf8.DecodeRune(b.pending)
			b.pending = b.pending[size:]
			if r == utf8.RuneError && size == 1 {
				continue
			}
			b.consume(r)
		}
	}
}
func (b *Buffer) consume(r rune) {
	switch r {
	case 0x1b:
		b.state = 'e'
		return
	case 0x9b:
		b.state = 'c'
		b.csi = b.csi[:0]
		b.csiOverflow = false
		return
	case 0x9d, 0x90, 0x98, 0x9e, 0x9f:
		b.state = 's'
		return
	case '\r':
		b.carriage = true
		return
	case '\n':
		line := string(b.current[b.head:])
		b.lines = append(b.lines, line)
		b.bytes += len(line) + 1
		if !b.plainOnly {
			style := b.currentStyle.normalized(b.head)
			b.styles = append(b.styles, style)
			b.styleBytes += style.extra()
		}
		b.current = nil
		b.currentStyle = lineStyle{}
		b.currentApplied = ""
		b.head = 0
		b.currentBytes = 0
		b.carriage = false
		b.trim()
		return
	}
	if unicode.IsControl(r) && r != '\t' {
		return
	}
	if b.carriage {
		b.current = nil
		b.currentStyle = lineStyle{}
		b.currentApplied = ""
		b.head = 0
		b.currentBytes = 0
		b.carriage = false
	}
	if !b.plainOnly {
		style := b.activeCode
		if len(b.current) == b.head {
			b.currentStyle.setPrefix(style)
			b.currentApplied = style
		} else if style != b.currentApplied {
			b.currentStyle.add(len(b.current), style)
			b.currentApplied = style
		}
	}
	b.current = append(b.current, r)
	b.currentBytes += utf8.RuneLen(r)
	b.trim()
}
func (b *Buffer) size() int { return b.bytes + b.currentBytes + b.styleBytes + b.currentStyle.extra() }
func (b *Buffer) trim() {
	for len(b.lines) > 0 && (len(b.lines)+1 > b.maxLines || b.size() > b.maxBytes) {
		b.bytes -= len(b.lines[0]) + 1
		b.lines[0] = ""
		b.lines = b.lines[1:]
		if !b.plainOnly {
			b.styleBytes -= b.styles[0].extra()
			b.styles[0] = lineStyle{}
			b.styles = b.styles[1:]
		}
		b.truncated = true
	}
	for b.size() > b.maxBytes && b.head < len(b.current) {
		b.currentBytes -= utf8.RuneLen(b.current[b.head])
		b.head++
		b.currentStyle.trimBefore(b.head)
		if b.head == len(b.current) {
			b.currentStyle = lineStyle{}
			b.currentApplied = ""
		}
		b.truncated = true
	}
	if b.head > 4096 && b.head >= len(b.current)/2 {
		b.current = append([]rune(nil), b.current[b.head:]...)
		b.currentStyle = b.currentStyle.normalized(b.head)
		b.head = 0
	}
}
func (b *Buffer) Plain() string {
	if b == nil {
		return ""
	}
	current := string(b.current[b.head:])
	if len(b.lines) == 0 {
		return current
	}
	return strings.Join(b.lines, "\n") + "\n" + current
}
func (b *Buffer) Lines() []string { return strings.Split(b.Plain(), "\n") }

// BufferedBytes counts retained text and style data, excluding Go allocation
// overhead. A nil buffer contributes nothing to a containing cache's budget.
func (b *Buffer) BufferedBytes() int {
	if b == nil {
		return 0
	}
	return b.size()
}

// StyledLines contains only safe, application-canonicalized SGR sequences. Each
// styled line is reset before its end; Plain remains the source for copying.
func (b *Buffer) StyledLines() []string {
	if b == nil {
		return []string{""}
	}
	if b.plainOnly {
		return b.Lines()
	}
	out := make([]string, 0, len(b.lines)+1)
	for i, line := range b.lines {
		out = append(out, styledLine(line, b.styles[i]))
	}
	out = append(out, styledLine(string(b.current[b.head:]), b.currentStyle.normalized(b.head)))
	return out
}
func (b *Buffer) Truncated() bool             { return b != nil && b.truncated }
func (b *Buffer) Write(p []byte) (int, error) { b.Append(string(p)); return len(p), nil }

// Sanitize strips escape sequences/control strings, preserves text/newlines and
// tabs, and normalizes carriage-return progress updates. Use Buffer for streams
// so escape sequences and UTF-8 split across transport chunks remain safe.
func Sanitize(text string) string {
	b := NewBuffer(len(text)+1, len(text)+1)
	b.plainOnly = true
	b.Append(text)
	return b.Plain()
}
