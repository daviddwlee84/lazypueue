package logview

import (
	"encoding/json"
	"fmt"
	"math"
	"os"
	"regexp"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/daviddwlee84/lazypueue/internal/core"
)

var levelRE = regexp.MustCompile(`(?i)^\s*(?:(?:\d{4}-\d{2}-\d{2}[T ][0-9:.+Z-]+|\d{2}:\d{2}:\d{2}(?:[.,]\d+)?)\s+)?\[?(trace|debug|info|warn(?:ing)?|error|fatal|critical|panic)\]?(?:\s*[:|\-]|\s|$)`)
var exceptionRE = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_.]*(?:Error|Exception):`)

func Severity(line string) string {
	line = strings.TrimSpace(Sanitize(line))
	var record map[string]json.RawMessage
	if strings.HasPrefix(line, "{") && json.Unmarshal([]byte(line), &record) == nil {
		for _, key := range []string{"level", "severity", "log_level"} {
			var v string
			if raw, ok := record[key]; ok && json.Unmarshal(raw, &v) == nil {
				return normalizeLevel(v)
			}
		}
	}
	if matches := levelRE.FindStringSubmatch(line); matches != nil {
		return normalizeLevel(matches[1])
	}
	if strings.HasPrefix(line, "Traceback (most recent call last):") || exceptionRE.MatchString(line) {
		return "error"
	}
	return ""
}
func normalizeLevel(level string) string {
	switch strings.ToLower(level) {
	case "fatal", "critical", "panic", "error", "err":
		return "error"
	case "warn", "warning":
		return "warning"
	case "debug", "trace":
		return "debug"
	case "info", "information":
		return "info"
	}
	return ""
}

// RenderLine adds only application-generated SGR sequences. Copy/export should
// use Plain/Sanitize instead. Query highlighting is literal and case-insensitive.
func RenderLine(line, query string, noColor bool) string {
	text := strings.ReplaceAll(Sanitize(line), "\t", "    ")
	if noColor || os.Getenv("NO_COLOR") != "" {
		return text
	}
	style := ""
	switch Severity(text) {
	case "error":
		style = "\x1b[31m"
	case "warning":
		style = "\x1b[33m"
	case "debug":
		style = "\x1b[90m"
	case "info":
		style = "\x1b[36m"
	}
	if query != "" {
		re, err := regexp.Compile("(?i)" + regexp.QuoteMeta(Sanitize(query)))
		if err == nil {
			var out strings.Builder
			last := 0
			for _, loc := range re.FindAllStringIndex(text, -1) {
				if loc[0] == loc[1] {
					continue
				}
				out.WriteString(text[last:loc[0]])
				out.WriteString("\x1b[30;43m")
				out.WriteString(text[loc[0]:loc[1]])
				out.WriteString("\x1b[0m" + style)
				last = loc[1]
			}
			out.WriteString(text[last:])
			text = out.String()
		}
	}
	if style != "" {
		return style + text + "\x1b[0m"
	}
	return text
}

// RenderStyledLine prefers safe source colors. Search deliberately uses plain
// severity highlighting so matches stay legible. Resets on both sides prevent
// source styling from leaking into pane borders or neighboring terminal text.
func RenderStyledLine(line, query string, noColor bool) string {
	if noColor || os.Getenv("NO_COLOR") != "" {
		return RenderLine(Sanitize(line), query, true)
	}
	if query != "" {
		return "\x1b[0m" + RenderLine(Sanitize(line), query, false) + "\x1b[0m"
	}
	b := NewBuffer(len(line)+1, max(2*1024*1024, len(line)*8+64))
	b.Append(line)
	styled := strings.ReplaceAll(strings.Join(b.StyledLines(), "\n"), "\t", "    ")
	if !strings.Contains(styled, "\x1b[") {
		return "\x1b[0m" + RenderLine(b.Plain(), "", false) + "\x1b[0m"
	}
	return "\x1b[0m" + styled + "\x1b[0m"
}

type Progress struct {
	Percent        float64
	Current, Total int64
	Source         string
	ObservedAt     time.Time
}

var tqdmRE = regexp.MustCompile(`(?i)^\s*(?:[^:\n]{1,80}:\s*)?(\d{1,3}(?:\.\d+)?)%\s*\|[^|\n]*\|\s*(\d+)\s*/\s*(\d+)(?:\s|\[|$)`)
var percentRE = regexp.MustCompile(`(?i)^\s*(?:progress|completion|completed)\s*[:=]\s*(\d{1,3}(?:\.\d+)?)\s*%(?:\s+(?:done|complete|completed))?\s*$`)
var ratioRE = regexp.MustCompile(`(?i)^\s*(?:(?:progress|steps?|epochs?|batches?|items?|processed)\s*[:=]?\s*)?(\d+)\s*/\s*(\d+)(?:\s+(?:done|complete|completed))?\s*$`)

func ParseProgress(text string, observedAt time.Time) (Progress, bool) {
	lines := strings.Split(Sanitize(text), "\n")
	for i := len(lines) - 1; i >= max(0, len(lines)-40); i-- {
		line := strings.TrimSpace(lines[i])
		p := Progress{Source: line, ObservedAt: observedAt}
		if m := tqdmRE.FindStringSubmatch(line); m != nil {
			var currentErr, totalErr error
			p.Percent, _ = strconv.ParseFloat(m[1], 64)
			p.Current, currentErr = strconv.ParseInt(m[2], 10, 64)
			p.Total, totalErr = strconv.ParseInt(m[3], 10, 64)
			if currentErr == nil && totalErr == nil && validProgress(p, true) && math.Abs(p.Percent-float64(p.Current)*100/float64(p.Total)) <= 1.1 {
				return p, true
			}
		}
		if m := percentRE.FindStringSubmatch(line); m != nil {
			p.Percent, _ = strconv.ParseFloat(m[1], 64)
			if validProgress(p, false) {
				return p, true
			}
		}
		if m := ratioRE.FindStringSubmatch(line); m != nil {
			var err error
			p.Current, err = strconv.ParseInt(m[1], 10, 64)
			if err != nil {
				continue
			}
			p.Total, err = strconv.ParseInt(m[2], 10, 64)
			if err != nil || p.Total <= 0 || p.Current > p.Total {
				continue
			}
			p.Percent = float64(p.Current) * 100 / float64(p.Total)
			if validProgress(p, true) {
				return p, true
			}
		}
	}
	return Progress{}, false
}
func validProgress(p Progress, ratio bool) bool {
	return !math.IsNaN(p.Percent) && !math.IsInf(p.Percent, 0) && p.Percent >= 0 && p.Percent <= 100 && (!ratio || (p.Total > 0 && p.Current >= 0 && p.Current <= p.Total))
}

func bounded(s string, n int) string {
	if len(s) <= n {
		return s
	}
	end := n
	for end > 0 && !utf8.RuneStart(s[end]) {
		end--
	}
	return s[:end] + "…"
}
func FailureReport(c core.Connection, t core.Task, text string, observedAt time.Time) string {
	command := t.OriginalCommand
	if command == "" {
		command = t.Command
	}
	field := func(s string) string { return bounded(Sanitize(s), 4096) }
	var out strings.Builder
	fmt.Fprintf(&out, "Connection: %s (%s)\nTask: #%d · %s\nCommand: %s\nDirectory: %s\nGroup: %s\n", field(c.DisplayName()), field(c.ID), t.ID, t.State(), field(command), field(t.Path), field(t.Group))
	if t.ExitCode != nil {
		fmt.Fprintf(&out, "Exit code: %d\n", *t.ExitCode)
	}
	if t.Result != "" {
		fmt.Fprintf(&out, "Result: %s\n", field(t.Result))
	}
	if t.Error != "" {
		fmt.Fprintf(&out, "Error: %s\n", field(t.Error))
	}
	if !t.CreatedAt.IsZero() {
		fmt.Fprintf(&out, "Created: %s\n", t.CreatedAt.Format(time.RFC3339))
	}
	if t.StartedAt != nil {
		fmt.Fprintf(&out, "Started: %s\n", t.StartedAt.Format(time.RFC3339))
	}
	if t.EndedAt != nil {
		fmt.Fprintf(&out, "Ended: %s\n", t.EndedAt.Format(time.RFC3339))
	}
	if !observedAt.IsZero() {
		fmt.Fprintf(&out, "Log observed: %s\n", observedAt.Format(time.RFC3339))
	}
	log := Sanitize(text)
	lines := strings.Split(strings.TrimSuffix(log, "\n"), "\n")
	truncated := len(lines) > 500
	if truncated {
		lines = lines[len(lines)-500:]
	}
	log = strings.Join(lines, "\n")
	remaining := 256*1024 - out.Len() - 128
	if len(log) > remaining {
		start := len(log) - remaining
		for start < len(log) && !utf8.RuneStart(log[start]) {
			start++
		}
		log = log[start:]
		truncated = true
	}
	if truncated {
		out.WriteString("Log tail (earlier output omitted; limited to 500 lines / 256 KiB):\n")
	} else {
		out.WriteString("Log output (loaded content):\n")
	}
	if log == "" {
		log = "(no output)"
	}
	out.WriteString(log)
	out.WriteByte('\n')
	return out.String()
}
