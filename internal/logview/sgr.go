package logview

import (
	"strconv"
	"strings"
)

// The allowlist intentionally excludes blinking, conceal, font switching and
// all non-SGR terminal commands. Colors are canonicalized to bounded full-state
// spans, so a line can be rendered independently without inheriting its frame.
type sgrState struct {
	foreground, background string
	flags                  uint8
}

func (s sgrState) sequence() string {
	if s.foreground == "" && s.background == "" && s.flags == 0 {
		return ""
	}
	parts := []string{"0"}
	for _, entry := range []struct {
		bit  uint8
		code string
	}{{1, "1"}, {2, "2"}, {4, "3"}, {8, "4"}, {16, "7"}, {32, "9"}} {
		if s.flags&entry.bit != 0 {
			parts = append(parts, entry.code)
		}
	}
	if s.foreground != "" {
		parts = append(parts, s.foreground)
	}
	if s.background != "" {
		parts = append(parts, s.background)
	}
	return "\x1b[" + strings.Join(parts, ";") + "m"
}
func applySGR(state sgrState, parameters []byte) (sgrState, bool) {
	if len(parameters) > 128 {
		return state, false
	}
	raw := strings.Split(string(parameters), ";")
	values := make([]int, len(raw))
	for i, p := range raw {
		if p == "" {
			values[i] = 0
			continue
		}
		n, err := strconv.Atoi(p)
		if err != nil || n < 0 || n > 255 {
			return state, false
		}
		values[i] = n
	}
	next := state
	for i := 0; i < len(values); i++ {
		value := values[i]
		switch {
		case value == 0:
			next = sgrState{}
		case value == 1:
			next.flags |= 1
		case value == 2:
			next.flags |= 2
		case value == 3:
			next.flags |= 4
		case value == 4:
			next.flags |= 8
		case value == 7:
			next.flags |= 16
		case value == 9:
			next.flags |= 32
		case value == 22:
			next.flags &^= 3
		case value == 23:
			next.flags &^= 4
		case value == 24:
			next.flags &^= 8
		case value == 27:
			next.flags &^= 16
		case value == 29:
			next.flags &^= 32
		case value == 39:
			next.foreground = ""
		case value == 49:
			next.background = ""
		case value >= 30 && value <= 37 || value >= 90 && value <= 97:
			next.foreground = strconv.Itoa(value)
		case value >= 40 && value <= 47 || value >= 100 && value <= 107:
			next.background = strconv.Itoa(value)
		case value == 38 || value == 48:
			count := 0
			if i+1 < len(values) {
				switch values[i+1] {
				case 5:
					count = 2
				case 2:
					count = 4
				}
			}
			if count == 0 || i+count >= len(values) {
				return state, false
			}
			parts := make([]string, count+1)
			for j := range parts {
				parts[j] = strconv.Itoa(values[i+j])
			}
			color := strings.Join(parts, ";")
			if value == 38 {
				next.foreground = color
			} else {
				next.background = color
			}
			i += count
		default:
			return state, false
		}
	}
	return next, true
}

type styleEvent struct {
	at   int
	code string
}
type lineStyle struct {
	prefix    string
	events    []styleEvent
	codeBytes int
}

func (s lineStyle) extra() int {
	if s.codeBytes == 0 {
		return 0
	}
	return s.codeBytes + 4
}
func (s *lineStyle) setPrefix(prefix string) {
	s.codeBytes += len(prefix) - len(s.prefix)
	s.prefix = prefix
}
func (s *lineStyle) add(at int, code string) {
	if code == "" {
		code = "\x1b[0m"
	}
	s.events = append(s.events, styleEvent{at, code})
	s.codeBytes += len(code)
}
func (s *lineStyle) trimBefore(head int) {
	for len(s.events) > 0 && s.events[0].at <= head {
		event := s.events[0]
		s.events[0] = styleEvent{}
		s.events = s.events[1:]
		s.codeBytes -= len(event.code)
		prefix := event.code
		if prefix == "\x1b[0m" {
			prefix = ""
		}
		s.setPrefix(prefix)
	}
}
func (s lineStyle) normalized(head int) lineStyle {
	if head == 0 {
		return s
	}
	events := append([]styleEvent(nil), s.events...)
	for i := range events {
		events[i].at -= head
	}
	s.events = events
	return s
}
func styledLine(text string, style lineStyle) string {
	if style.codeBytes == 0 {
		return text
	}
	var out strings.Builder
	out.Grow(len(text) + style.extra())
	out.WriteString(style.prefix)
	event := 0
	index := 0
	for _, r := range text {
		for event < len(style.events) && style.events[event].at == index {
			out.WriteString(style.events[event].code)
			event++
		}
		out.WriteRune(r)
		index++
	}
	out.WriteString("\x1b[0m")
	return out.String()
}
