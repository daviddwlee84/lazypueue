package pueue

import (
	"encoding/json"
	"fmt"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/daviddwlee84/lazypueue/internal/core"
)

type wireTask struct {
	ID              int             `json:"id"`
	Command         string          `json:"command"`
	OriginalCommand string          `json:"original_command"`
	Label           string          `json:"label"`
	Group           string          `json:"group"`
	Path            string          `json:"path"`
	Priority        int             `json:"priority"`
	Dependencies    []int           `json:"dependencies"`
	CreatedAt       time.Time       `json:"created_at"`
	Status          json.RawMessage `json:"status"`
	// envs is deliberately not represented or retained.
}

type statusFields struct {
	EnqueuedAt     *time.Time      `json:"enqueued_at"`
	EnqueueAt      *time.Time      `json:"enqueue_at"`
	Start          *time.Time      `json:"start"`
	End            *time.Time      `json:"end"`
	Result         json.RawMessage `json:"result"`
	PreviousStatus json.RawMessage `json:"previous_status"`
}

// ParseSnapshot normalizes Pueue v4's externally tagged enums at the boundary.
// Unknown future status/result variants remain visible and non-actionable.
func ParseSnapshot(data []byte, connectionID string, now time.Time) (core.Snapshot, error) {
	var raw struct {
		Tasks  map[string]wireTask `json:"tasks"`
		Groups map[string]struct {
			Status   string `json:"status"`
			Parallel int    `json:"parallel_tasks"`
		} `json:"groups"`
	}
	if err := json.Unmarshal(data, &raw); err != nil {
		return core.Snapshot{}, fmt.Errorf("invalid Pueue status JSON: %w", err)
	}
	if raw.Tasks == nil || raw.Groups == nil {
		return core.Snapshot{}, fmt.Errorf("expected Pueue v4 status JSON with tasks and groups")
	}
	result := core.Snapshot{ConnectionID: connectionID, ObservedAt: now, Tasks: []core.Task{}, Groups: []core.Group{}}
	for key, r := range raw.Tasks {
		if strconv.Itoa(r.ID) != key {
			return core.Snapshot{}, fmt.Errorf("Pueue returned inconsistent task ID %q", key)
		}
		t := core.Task{ID: r.ID, Command: r.Command, OriginalCommand: r.OriginalCommand, Label: r.Label, Group: r.Group, Path: r.Path, Priority: r.Priority, Dependencies: r.Dependencies, CreatedAt: r.CreatedAt}
		if err := normalizeStatus(&t, r.Status, 0); err != nil {
			return core.Snapshot{}, fmt.Errorf("task #%d: %w", r.ID, err)
		}
		result.Tasks = append(result.Tasks, t)
	}
	sort.Slice(result.Tasks, func(i, j int) bool { return result.Tasks[i].ID < result.Tasks[j].ID })
	for name, g := range raw.Groups {
		result.Groups = append(result.Groups, core.Group{Name: name, Status: strings.ToLower(g.Status), Parallel: g.Parallel})
	}
	sort.Slice(result.Groups, func(i, j int) bool { return result.Groups[i].Name < result.Groups[j].Name })
	return result, nil
}

func normalizeStatus(t *core.Task, raw json.RawMessage, depth int) error {
	if depth > 16 {
		return fmt.Errorf("too many nested task locks")
	}
	var tags map[string]json.RawMessage
	if err := json.Unmarshal(raw, &tags); err != nil {
		return fmt.Errorf("unsupported status shape; Pueue 4.x is required")
	}
	if len(tags) != 1 {
		return fmt.Errorf("status must have exactly one variant")
	}
	for tag, payload := range tags {
		var fields statusFields
		switch tag {
		case "Stashed", "Queued", "Running", "Paused", "Locked", "Done":
			if err := json.Unmarshal(payload, &fields); err != nil {
				return fmt.Errorf("invalid %s status: %w", tag, err)
			}
		default:
			t.Status = "unknown"
			return nil
		}
		if tag == "Locked" {
			t.Locked = true
			return normalizeStatus(t, fields.PreviousStatus, depth+1)
		}
		t.Status = strings.ToLower(tag)
		t.EnqueuedAt = fields.EnqueuedAt
		t.ScheduledAt = fields.EnqueueAt
		t.StartedAt = fields.Start
		t.EndedAt = fields.End
		if tag == "Done" {
			var result string
			if json.Unmarshal(fields.Result, &result) == nil {
				switch result {
				case "Success":
					t.Result = "success"
				case "Killed":
					t.Result = "killed"
				case "Errored":
					t.Result = "errored"
				case "DependencyFailed":
					t.Result = "dependency-failed"
				default:
					t.Result = "unknown"
				}
			} else {
				var variants map[string]json.RawMessage
				if json.Unmarshal(fields.Result, &variants) != nil || len(variants) != 1 {
					t.Result = "unknown"
					continue
				}
				if value, ok := variants["Failed"]; ok {
					var code int
					if err := json.Unmarshal(value, &code); err != nil {
						return fmt.Errorf("invalid failure exit code")
					}
					t.Result = "failed"
					t.ExitCode = &code
				} else if value, ok := variants["FailedToSpawn"]; ok {
					t.Result = "failed-to-spawn"
					if err := json.Unmarshal(value, &t.Error); err != nil {
						return fmt.Errorf("invalid spawn failure")
					}
				} else {
					t.Result = "unknown"
				}
			}
		}
	}
	return nil
}
