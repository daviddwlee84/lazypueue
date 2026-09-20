package core

import (
	"context"
	"time"
)

// Connection references credentials by file path; it never contains their contents.
type Connection struct {
	ID         string `toml:"id" json:"id"`
	Name       string `toml:"name,omitempty" json:"name,omitempty"`
	Kind       string `toml:"kind" json:"kind"`
	Binary     string `toml:"binary,omitempty" json:"binary,omitempty"`
	ConfigPath string `toml:"config_path,omitempty" json:"config_path,omitempty"`
	Profile    string `toml:"profile,omitempty" json:"profile,omitempty"`
	SSHHost    string `toml:"ssh_host,omitempty" json:"ssh_host,omitempty"`
	SSHBinary  string `toml:"ssh_binary,omitempty" json:"ssh_binary,omitempty"`
	SSHConfig  string `toml:"ssh_config,omitempty" json:"ssh_config,omitempty"`
	SSHProfile string `toml:"ssh_profile,omitempty" json:"ssh_profile,omitempty"`
	Host       string `toml:"host,omitempty" json:"host,omitempty"`
	Port       int    `toml:"port,omitempty" json:"port,omitempty"`
	CertPath   string `toml:"cert_path,omitempty" json:"cert_path,omitempty"`
	SecretPath string `toml:"secret_path,omitempty" json:"secret_path,omitempty"`
	SocketPath string `toml:"socket_path,omitempty" json:"socket_path,omitempty"`
}

func (c Connection) DisplayName() string {
	if c.Name != "" {
		return c.Name
	}
	return c.ID
}

// Task is an allowlist of the public Pueue task fields. Captured envs are excluded.
type Task struct {
	ID              int        `json:"id"`
	Command         string     `json:"command"`
	OriginalCommand string     `json:"original_command,omitempty"`
	Locked          bool       `json:"locked,omitempty"`
	Label           string     `json:"label,omitempty"`
	Group           string     `json:"group"`
	Path            string     `json:"path"`
	Status          string     `json:"status"`
	Result          string     `json:"result,omitempty"`
	Error           string     `json:"error,omitempty"`
	ExitCode        *int       `json:"exit_code,omitempty"`
	Priority        int        `json:"priority"`
	Dependencies    []int      `json:"dependencies,omitempty"`
	CreatedAt       time.Time  `json:"created_at"`
	EnqueuedAt      *time.Time `json:"enqueued_at,omitempty"`
	ScheduledAt     *time.Time `json:"scheduled_at,omitempty"`
	StartedAt       *time.Time `json:"started_at,omitempty"`
	EndedAt         *time.Time `json:"ended_at,omitempty"`
}

type Group struct {
	Name     string `json:"name"`
	Status   string `json:"status"`
	Parallel int    `json:"parallel"`
}

type Snapshot struct {
	ConnectionID string    `json:"connection"`
	Tasks        []Task    `json:"tasks"`
	Groups       []Group   `json:"groups"`
	ObservedAt   time.Time `json:"observed_at"`
}

type AddRequest struct {
	Command       string `json:"command"`
	Directory     string `json:"directory"`
	Group         string `json:"group"`
	Label         string `json:"label,omitempty"`
	Mode          string `json:"mode"` // queued, stashed, immediate
	After         []int  `json:"after,omitempty"`
	Delay         string `json:"delay,omitempty"`
	Priority      int    `json:"priority"`
	CreateGroup   bool   `json:"create_group,omitempty"`
	GroupParallel int    `json:"group_parallel,omitempty"`
}

type Request struct {
	Operation      string            `json:"operation"`
	IDs            []int             `json:"ids,omitempty"`
	Guards         map[int]time.Time `json:"-"`
	Group          string            `json:"group,omitempty"`
	Parallel       int               `json:"parallel,omitempty"`
	InPlace        bool              `json:"in_place,omitempty"`
	ResumeOnly     bool              `json:"resume_only,omitempty"`
	SuccessfulOnly bool              `json:"successful_only,omitempty"`
	Add            *AddRequest       `json:"add,omitempty"`
}

type Outcome struct {
	ID    int    `json:"id"`
	Error string `json:"error,omitempty"`
}

type Result struct {
	ConnectionID string    `json:"connection"`
	TaskID       *int      `json:"task_id,omitempty"`
	Message      string    `json:"message"`
	Unknown      bool      `json:"unknown,omitempty"`
	Outcomes     []Outcome `json:"outcomes,omitempty"`
}

type Plan struct {
	ConnectionID string   `json:"connection"`
	Transport    string   `json:"transport"`
	Display      string   `json:"command"`
	Consequences []string `json:"consequences,omitempty"`
}

type Backend interface {
	Snapshot(context.Context, Connection) (Snapshot, error)
	Log(context.Context, Connection, int, int) (string, error)
	Follow(context.Context, Connection, int, int, func(string)) error
	Preview(Connection, Request) (Plan, error)
	Execute(context.Context, Connection, Request) (Result, error)
}
