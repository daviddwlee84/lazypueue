package logview

import (
	"strings"
	"testing"
	"time"
	"unicode/utf8"

	"github.com/daviddwlee84/lazypueue/internal/core"
)

func TestSafeSourceSGRSurvivesSplitChunksButPlainAndReportStayPlain(t *testing.T) {
	b := NewBuffer(100, 10000)
	for _, chunk := range []string{"before \x1b[3", "1mred", " word\x1b[0", "m after\n"} {
		b.Append(chunk)
	}
	if b.Plain() != "before red word after\n" {
		t.Fatalf("plain: %q", b.Plain())
	}
	line := b.StyledLines()[0]
	if !strings.Contains(line, "\x1b[0;31mred word") || !strings.HasSuffix(line, "\x1b[0m") {
		t.Fatalf("safe color/reset missing: %q", line)
	}
	if Sanitize(line) != "before red word after" {
		t.Fatal("styling changed text")
	}
	report := FailureReport(core.Connection{ID: "local"}, core.Task{ID: 1, Status: "failed"}, strings.Join(b.StyledLines(), "\n"), time.Now())
	if strings.ContainsRune(report, 0x1b) || !strings.Contains(report, "red word") {
		t.Fatalf("report retained control sequences: %q", report)
	}
}
func TestUnsafeSGRAndControlStringsNeverSurvive(t *testing.T) {
	b := NewBuffer(100, 10000)
	for _, chunk := range []string{"\x1b[5mblink\x1b[8mhide\x1b[31;8mconceal\x1b[0m", "\x1b]52;c;payload", "\x1b\\", "\x1b[2J\x1b[3;4Hvisible"} {
		b.Append(chunk)
	}
	styled := strings.Join(b.StyledLines(), "\n")
	if strings.Contains(styled, "payload") || strings.Contains(styled, "\x1b[5m") || strings.Contains(styled, "\x1b[8m") || strings.Contains(styled, "\x1b[2J") || strings.Contains(styled, "\x1b[3;4H") {
		t.Fatalf("unsafe control survived: %q", styled)
	}
	if b.Plain() != "blinkhideconcealvisible" {
		t.Fatalf("unexpected text: %q", b.Plain())
	}
}
func TestCRReplacesOldStyledLineAndCarriesOnlyActiveSafeStyle(t *testing.T) {
	b := NewBuffer(100, 10000)
	b.Append("\x1b[31mold\r")
	b.Append("new\nnext\x1b[0m normal")
	lines := b.StyledLines()
	if b.Plain() != "new\nnext normal" || strings.Contains(strings.Join(lines, ""), "old") {
		t.Fatal(b.Plain())
	}
	for _, line := range lines {
		if !strings.HasPrefix(line, "\x1b[0;31m") {
			t.Fatalf("active color not carried: %q", line)
		}
	}
	b.Append("\r\x1b[32mgreen")
	last := b.StyledLines()[1]
	if strings.Contains(last, "31m") || !strings.Contains(last, "32mgreen") {
		t.Fatalf("old line style survived CR: %q", last)
	}
}
func TestStyledRenderingResetsAndNoColorAndSearchRemainPlainSafe(t *testing.T) {
	t.Setenv("NO_COLOR", "")
	line := "\x1b[38;2;10;20;30mred\x1b[48;5;20m background\x1b[0m"
	rendered := RenderStyledLine(line, "", false)
	if !strings.Contains(rendered, "38;2;10;20;30") || !strings.Contains(rendered, "48;5;20") || !strings.HasPrefix(rendered, "\x1b[0m") || !strings.HasSuffix(rendered, "\x1b[0m") {
		t.Fatalf("styles/reset lost: %q", rendered)
	}
	if RenderStyledLine(line, "red", true) != "red background" {
		t.Fatal("no-color changed output")
	}
	search := RenderStyledLine(line, "red", false)
	if strings.Contains(search, "38;2;") || !strings.Contains(search, "30;43mred") {
		t.Fatalf("search did not prioritize match: %q", search)
	}
	t.Setenv("NO_COLOR", "1")
	if strings.ContainsRune(RenderStyledLine(line, "", false), 0x1b) {
		t.Fatal("NO_COLOR retained styles")
	}
}
func TestStyleSpamAndLongColoredLinesShareTheByteBound(t *testing.T) {
	b := NewBuffer(50, 256)
	b.Append(strings.Repeat("\x1b[31m\x1b[32m", 10000))
	if b.Plain() != "" || len(b.StyledLines()[0]) != 0 {
		t.Fatal("empty style spam created output")
	}
	b.Append(strings.Repeat("\x1b[31m界\x1b[32mx", 10000))
	plain := b.Plain()
	styled := strings.Join(b.StyledLines(), "\n")
	if len(styled) > 256 || len(plain) > 256 || !utf8.ValidString(styled) || !b.Truncated() {
		t.Fatalf("unbounded styled log: plain=%d styled=%d", len(plain), len(styled))
	}
	if Sanitize(styled) != plain {
		t.Fatal("trimming changed style/text alignment")
	}
	b.Replace("\x1b[31m" + strings.Repeat("colored\n", 100))
	styled = strings.Join(b.StyledLines(), "\n")
	if len(styled) > 256 || Sanitize(styled) != b.Plain() {
		t.Fatalf("multi-line style bound/alignment: %d", len(styled))
	}
	text := "\x1b[31m" + strings.Repeat("line\n", 10000)
	if Sanitize(text) != strings.Repeat("line\n", 10000) {
		t.Fatal("plain sanitize was truncated by style overhead")
	}
}
