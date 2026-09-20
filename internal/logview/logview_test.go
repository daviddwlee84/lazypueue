package logview

import (
	"strings"
	"testing"
	"time"
	"unicode/utf8"

	"github.com/daviddwlee84/lazypueue/internal/core"
)

func TestBufferSplitCRANSIAndUTF8(t *testing.T) {
	b := NewBuffer(100, 10000)
	for _, s := range []string{"step 1\r", "step 2\r", "\n", "safe\x1b]52;c;", "c2VjcmV0\x1b", "\\next\n", "\x1b[3", "1mError: red\x1b[0m\n", "\xe4", "\xb8\xad"} {
		b.Append(s)
	}
	want := "step 2\nsafenext\nError: red\n中"
	if b.Plain() != want {
		t.Fatalf("got %q want %q", b.Plain(), want)
	}
	if strings.Contains(b.Plain(), "c2VjcmV0") || strings.ContainsRune(b.Plain(), 0x1b) {
		t.Fatal("control string leaked")
	}
}
func TestBufferHugeLineAndBounds(t *testing.T) {
	b := NewBuffer(3, 64)
	b.Append(strings.Repeat("中", 100000))
	if len(b.Plain()) > 64 || !utf8.ValidString(b.Plain()) || !b.Truncated() {
		t.Fatalf("unbounded/invalid buffer %d", len(b.Plain()))
	}
	b.Replace("one\ntwo\nthree\nfour")
	if len(b.Lines()) > 3 || !strings.HasSuffix(b.Plain(), "four") {
		t.Fatal(b.Plain())
	}
	b.Replace("fresh")
	if b.Truncated() {
		t.Fatal("replace retained truncation state")
	}
}
func TestProgressStrictRecognition(t *testing.T) {
	now := time.Now()
	valid := []string{"train: 25%|██      | 5/20 [00:01<00:03]", "Progress: 47.5%", "Epoch 3/10", "4/8", "old\rProgress: 70%"}
	for _, s := range valid {
		p, ok := ParseProgress(s, now)
		if !ok || p.Percent < 0 || p.Percent > 100 || !p.ObservedAt.Equal(now) {
			t.Errorf("rejected %q: %#v", s, p)
		}
	}
	for _, s := range []string{"accuracy: 98%", "loss=0.23", "progress: 101%", "Epoch 4/0", "Epoch 11/10", "HTTP/1.1 200 OK", "saved 3/10 files at 90% accuracy", "25%|██| 999999999999999999999/999999999999999999999", "25%|██| 9/10"} {
		if p, ok := ParseProgress(s, now); ok {
			t.Errorf("false progress %q: %#v", s, p)
		}
	}
}
func TestSeverityCopyAndNoColor(t *testing.T) {
	t.Setenv("NO_COLOR", "")
	if Severity(`{"level":"ERROR","message":"bad"}`) != "error" || Severity("2026-09-20 10:30:20 WARN: retry") != "warning" || Severity("ValueError: bad") != "error" {
		t.Fatal("severity missing")
	}
	if Severity("command mentions error=0") != "" {
		t.Fatal("unstructured substring was marked error")
	}
	plain := "ERROR: bad query"
	colored := RenderLine(plain, "bad", false)
	if !strings.Contains(colored, "\x1b[31m") || !strings.Contains(colored, "\x1b[30;43m") {
		t.Fatal("missing severity/search coloring")
	}
	if Sanitize(colored) != plain || RenderLine(plain, "bad", true) != plain {
		t.Fatal("color changed content")
	}
	t.Setenv("NO_COLOR", "1")
	if RenderLine(plain, "bad", false) != plain {
		t.Fatal("NO_COLOR ignored")
	}
}
func TestFailureReportBoundedAndNoCapturedSecrets(t *testing.T) {
	code := 2
	c := core.Connection{ID: "lab", Name: "Lab", SecretPath: "/private/secret"}
	task := core.Task{ID: 7, Status: "done", Result: "failed", Command: "echo failed", ExitCode: &code, Path: "/work", Group: "train"}
	report := FailureReport(c, task, strings.Repeat("older line\n", 600)+strings.Repeat("界", 100000), time.Now())
	if len(report) > 256*1024 || !utf8.ValidString(report) {
		t.Fatalf("report bounds %d", len(report))
	}
	for _, want := range []string{"Connection: Lab (lab)", "Task: #7", "Exit code: 2", "earlier output omitted"} {
		if !strings.Contains(report, want) {
			t.Errorf("missing %q", want)
		}
	}
	if strings.Contains(report, "/private/secret") {
		t.Fatal("credential reference leaked into report")
	}
}
