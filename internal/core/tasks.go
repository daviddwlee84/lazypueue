package core

import (
	"fmt"
	"math"
	"strings"
	"time"
)

// State is the user-facing state, including terminal result distinctions.
func (t Task) State() string {
	status := strings.ToLower(t.Status)
	if status == "done" {
		if strings.EqualFold(t.Result, "success") || strings.EqualFold(t.Result, "succeeded") {
			return "succeeded"
		}
		return "failed"
	}
	if status == "success" {
		return "succeeded"
	}
	return status
}

func (t Task) Terminal() bool {
	switch t.State() {
	case "succeeded", "failed":
		return true
	}
	return false
}

func (t Task) Failed() bool { return t.State() == "failed" }

// Identity distinguishes reused daemon IDs after a reset. Prefix with connection
// ID when retaining an identity outside a single connection's snapshot.
func (t Task) Identity() string {
	return fmt.Sprintf("%d@%s", t.ID, t.CreatedAt.Format(time.RFC3339Nano))
}

// ValidateAddPartial checks explicit values without requiring a complete form.
func ValidateAddPartial(a AddRequest) error {
	if strings.ContainsRune(a.Command, 0) || strings.ContainsRune(a.Directory, 0) || strings.ContainsRune(a.Label, 0) || strings.ContainsRune(a.Group, 0) || strings.ContainsRune(a.Delay, 0) {
		return fmt.Errorf("fields cannot contain NUL bytes")
	}
	switch a.Mode {
	case "", "queued", "stashed", "immediate":
	default:
		return fmt.Errorf("start mode must be queued, stashed, or immediate")
	}
	if a.Mode == "immediate" && strings.TrimSpace(a.Delay) != "" {
		return fmt.Errorf("an immediate task cannot also have a delay")
	}
	if a.Mode == "immediate" && len(a.After) > 0 {
		return fmt.Errorf("an immediate task cannot also have dependencies")
	}
	if a.Priority < math.MinInt32 || a.Priority > math.MaxInt32 {
		return fmt.Errorf("priority must fit a signed 32-bit integer")
	}
	if a.GroupParallel < 0 {
		return fmt.Errorf("group parallelism cannot be negative (0 means unlimited)")
	}
	seen := map[int]bool{}
	for _, id := range a.After {
		if id < 0 {
			return fmt.Errorf("dependency IDs cannot be negative")
		}
		if seen[id] {
			return fmt.Errorf("dependency #%d is repeated", id)
		}
		seen[id] = true
	}
	return nil
}

// ValidateAdd validates a complete wizard or script request. Group defaults to
// Pueue's default group and an empty mode means queued.
func ValidateAdd(a AddRequest) error {
	if err := ValidateAddPartial(a); err != nil {
		return err
	}
	if strings.TrimSpace(a.Command) == "" {
		return fmt.Errorf("a command is required")
	}
	if strings.TrimSpace(a.Directory) == "" {
		return fmt.Errorf("a working directory is required")
	}
	if a.CreateGroup && strings.TrimSpace(a.Group) == "" {
		return fmt.Errorf("name the group to create")
	}
	return nil
}

// Eligible is shared by keyboard hints and the backend's fresh-state checks.
func Eligible(operation string, t Task) bool {
	if t.Locked {
		return false
	}
	switch operation {
	case "restart", "restart-failed":
		return t.Terminal()
	case "pause":
		return t.State() == "running"
	case "start":
		return t.State() == "paused" || t.State() == "queued" || t.State() == "stashed"
	case "kill":
		return t.State() == "running" || t.State() == "paused"
	case "stash":
		return t.State() == "queued"
	case "enqueue":
		return t.State() == "stashed"
	case "remove":
		return t.Terminal() || t.State() == "stashed" || t.State() == "queued"
	case "clean":
		return t.Terminal()
	case "edit":
		return t.State() == "queued" || t.State() == "stashed"
	}
	return false
}

type Summary struct {
	Total       int            `json:"total"`
	Running     int            `json:"running"`
	Queued      int            `json:"queued"`
	Paused      int            `json:"paused"`
	Stashed     int            `json:"stashed"`
	Locked      int            `json:"locked"`
	Parallel    int            `json:"parallel"`
	Succeeded   int            `json:"succeeded"`
	Failed      int            `json:"failed"`
	Finished    int            `json:"finished"`
	Progress    float64        `json:"progress"`
	AvgDuration time.Duration  `json:"average_ns"`
	ETA         *time.Duration `json:"eta_ns,omitempty"`
	Elapsed     time.Duration  `json:"elapsed_ns"`
	FailedIDs   []int          `json:"failed_ids"`
}

// Summarize uses only the tasks currently retained by Pueue. It never invents a
// batch boundary. ETA is available for a running, bounded-parallelism group
// with at least two completed duration samples; paused/stashed work has no ETA.
func Summarize(snapshot Snapshot, group string, now time.Time) Summary {
	s := Summary{FailedIDs: []int{}}
	var totalDuration time.Duration
	var samples int
	var first, last time.Time
	for _, t := range snapshot.Tasks {
		if group != "" && t.Group != group {
			continue
		}
		s.Total++
		if t.Locked {
			s.Locked++
		}
		switch t.State() {
		case "running":
			s.Running++
		case "queued":
			s.Queued++
		case "paused":
			s.Paused++
		case "stashed":
			s.Stashed++
		case "succeeded":
			s.Succeeded++
		case "failed":
			s.Failed++
			s.FailedIDs = append(s.FailedIDs, t.ID)
		}
		if t.StartedAt != nil && (first.IsZero() || t.StartedAt.Before(first)) {
			first = *t.StartedAt
		}
		if t.EndedAt != nil && t.EndedAt.After(last) {
			last = *t.EndedAt
		}
		if t.Terminal() && t.StartedAt != nil && t.EndedAt != nil {
			d := t.EndedAt.Sub(*t.StartedAt)
			if d >= 0 {
				totalDuration += d
				samples++
			}
		}
	}
	s.Finished = s.Succeeded + s.Failed
	if s.Total > 0 {
		s.Progress = float64(s.Finished) / float64(s.Total)
	}
	if samples > 0 {
		s.AvgDuration = totalDuration / time.Duration(samples)
	}
	if s.Running > 0 {
		last = now
	}
	if !first.IsZero() && last.After(first) {
		s.Elapsed = last.Sub(first)
	}
	for _, g := range snapshot.Groups {
		if g.Name == group {
			s.Parallel = g.Parallel
		}
	}
	if group != "" && samples >= 2 && s.Paused == 0 && s.Running+s.Queued > 0 {
		for _, g := range snapshot.Groups {
			if g.Name == group && strings.EqualFold(g.Status, "running") && g.Parallel > 0 {
				eta := time.Duration(float64(s.AvgDuration) * float64(s.Running+s.Queued) / float64(g.Parallel))
				s.ETA = &eta
			}
		}
	}
	return s
}
