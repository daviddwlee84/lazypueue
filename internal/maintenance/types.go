// Package maintenance performs explicitly reviewed, idle-only Pueue upgrades.
// Queue operations and package/service management have separate authority.
package maintenance

import (
	"context"
	"io"
	"time"

	"github.com/daviddwlee84/lazypueue/internal/core"
)

type Target struct {
	SSHHost string `json:"ssh_host,omitempty"`
}
type Runner interface {
	Run(context.Context, Target, []string) ([]byte, error)
}
type Service struct {
	Backend   core.Backend
	Runner    Runner
	Candidate func(context.Context) (string, error)
	// ReceiptDir is injectable for isolated tests; empty uses XDG state.
	ReceiptDir string
}
type Plan struct {
	Connection       core.Connection `json:"connection"`
	Target           Target          `json:"target"`
	Binary           string          `json:"binary"`
	Resolved         string          `json:"resolved"`
	DaemonBinary     string          `json:"daemon_binary"`
	CurrentVersion   string          `json:"current_version"`
	CandidateVersion string          `json:"candidate_version,omitempty"`
	Owner            string          `json:"owner"`
	ManagerBinary    string          `json:"manager_binary,omitempty"`
	InstallRoot      string          `json:"install_root,omitempty"`
	ServiceKind      string          `json:"service_kind,omitempty"`
	ServiceName      string          `json:"service_name,omitempty"`
	ServicePID       int             `json:"service_pid,omitempty"`
	PIDPath          string          `json:"pid_path,omitempty"`
	WasRunning       bool            `json:"was_running"`
	Groups           []core.Group    `json:"groups"`
	ActiveIDs        []int           `json:"active_ids"`
	CanApply         bool            `json:"can_apply"`
	UpdateAvailable  bool            `json:"update_available"`
	Reason           string          `json:"reason,omitempty"`
	Review           []string        `json:"review"`
	CheckedAt        time.Time       `json:"checked_at"`
	checked          bool
	ready            bool
	identity         string
	queue            core.Connection
	uid              string
}
type Step struct {
	Name    string    `json:"name"`
	Command []string  `json:"command,omitempty"`
	State   string    `json:"state"`
	At      time.Time `json:"at"`
}
type Result struct {
	ConnectionID   string `json:"connection"`
	Status         string `json:"status"`
	Message        string `json:"message"`
	ReceiptPath    string `json:"receipt_path,omitempty"`
	LockPath       string `json:"lock_path,omitempty"`
	Steps          []Step `json:"steps"`
	Unknown        bool   `json:"unknown,omitempty"`
	PackageUpdated bool   `json:"package_updated"`
	DaemonStarted  bool   `json:"daemon_started"`
}

func New(backend core.Backend) *Service {
	return &Service{Backend: backend, Runner: commandRunner{}, Candidate: latestPueue}
}
func (s *Service) Apply(ctx context.Context, plan Plan, progress io.Writer) (Result, error) {
	return s.apply(ctx, plan, progress)
}
