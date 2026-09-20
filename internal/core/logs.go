package core

import (
	"context"
	"time"
)

// Empty settings inherit the containing application's defaults. PollInterval is
// a Go duration so settings remain readable (for example "10s" or "2m").
type LogSettings struct {
	SingleMode   string `toml:"single_mode,omitempty" json:"single_mode,omitempty"`
	MultiMode    string `toml:"multi_mode,omitempty" json:"multi_mode,omitempty"`
	PollInterval string `toml:"poll_interval,omitempty" json:"poll_interval,omitempty"`
	TailLines    int    `toml:"tail_lines,omitempty" json:"tail_lines,omitempty"`
}

type LogResult struct {
	Task       *Task     `json:"task,omitempty"`
	Output     string    `json:"output"`
	Error      string    `json:"error,omitempty"`
	ObservedAt time.Time `json:"observed_at"`
}

// BatchLogger extends existing backends without forcing test/offline adapters
// to implement batching. The Pueue adapter combines IDs into one CLI request.
type BatchLogger interface {
	Logs(context.Context, Connection, []int, int) (map[int]LogResult, error)
}

type EditRequest struct {
	Command   string `json:"command"`
	Directory string `json:"directory"`
	Label     string `json:"label,omitempty"`
	Priority  int    `json:"priority"`
	Stashed   bool   `json:"stashed"`
}

// AttemptIdentity separates an in-place retry from its previous output. The UI
// also observes terminal-to-queued transitions, during which no log is read.
func (t Task) AttemptIdentity() string {
	k := t.Identity()
	if t.StartedAt != nil {
		return k + "/" + t.StartedAt.Format(time.RFC3339Nano)
	}
	return k + "/pending"
}
