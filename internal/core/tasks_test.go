package core

import (
	"testing"
	"time"
)

func TestValidationAndEligibility(t *testing.T) {
	if err := ValidateAddPartial(AddRequest{}); err != nil {
		t.Fatal(err)
	}
	if err := ValidateAdd(AddRequest{}); err == nil {
		t.Fatal("empty complete form accepted")
	}
	for _, a := range []AddRequest{
		{Command: "echo hi", Directory: "/", Mode: "immediate", Delay: "1h"},
		{Command: "echo hi", Directory: "/", Mode: "immediate", After: []int{0}},
		{Command: "echo hi", Directory: "/", After: []int{1, 1}},
		{Command: "echo hi", Directory: "/", Priority: 1 << 40},
		{Command: "echo hi", Directory: "/", Mode: "typo"},
	} {
		if err := ValidateAdd(a); err == nil {
			t.Fatalf("invalid form accepted: %+v", a)
		}
	}
	if err := ValidateAdd(AddRequest{Command: "echo hi", Directory: "/", Mode: "queued", After: []int{0}, Priority: -2}); err != nil {
		t.Fatal(err)
	}
	if Eligible("kill", Task{Status: "done", Result: "success"}) {
		t.Fatal("finished task killable")
	}
	if Eligible("pause", Task{Status: "running", Locked: true}) {
		t.Fatal("locked task actionable")
	}
	if !Eligible("restart", Task{Status: "done", Result: "failed-to-spawn"}) {
		t.Fatal("spawn failure not restartable")
	}
}

func TestSummaryCountsAndHonestETA(t *testing.T) {
	start := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	end := start.Add(10 * time.Second)
	snapshot := Snapshot{Groups: []Group{{Name: "batch", Status: "running", Parallel: 2}}, Tasks: []Task{
		{ID: 1, Group: "batch", Status: "done", Result: "success", StartedAt: &start, EndedAt: &end},
		{ID: 2, Group: "batch", Status: "done", Result: "failed-to-spawn", StartedAt: &start, EndedAt: &end},
		{ID: 3, Group: "batch", Status: "queued", Locked: true},
		{ID: 4, Group: "batch", Status: "running", StartedAt: &end},
		{ID: 5, Group: "batch", Status: "stashed"},
		{ID: 6, Group: "elsewhere", Status: "queued"},
	}}
	s := Summarize(snapshot, "batch", end.Add(5*time.Second))
	if s.Total != 5 || s.Finished != 2 || s.Failed != 1 || s.Succeeded != 1 || s.Locked != 1 || s.Stashed != 1 || s.AvgDuration != 10*time.Second || s.ETA == nil || *s.ETA != 10*time.Second {
		t.Fatalf("unexpected summary: %+v", s)
	}
	if s.Elapsed != 15*time.Second || s.Progress != 0.4 || s.Parallel != 2 {
		t.Fatalf("unexpected timing: %+v", s)
	}
	snapshot.Groups[0].Parallel = 0
	if Summarize(snapshot, "batch", end).ETA != nil {
		t.Fatal("unlimited parallelism got misleading ETA")
	}
	snapshot.Groups[0].Parallel = 2
	snapshot.Groups[0].Status = "paused"
	if Summarize(snapshot, "batch", end).ETA != nil {
		t.Fatal("paused queue got ETA")
	}
	if Summarize(snapshot, "", end).ETA != nil {
		t.Fatal("cross-group ETA invented")
	}
}
