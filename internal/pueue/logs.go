package pueue

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os/exec"
	"strconv"
	"strings"
	"time"

	"github.com/daviddwlee84/lazypueue/internal/core"
)

var _ core.BatchLogger = (*Client)(nil)

// Logs returns replaceable tail snapshots, never appendable byte ranges.
// Batches are bounded, and only missing-log errors trigger one isolation pass.
func (c *Client) Logs(ctx context.Context, conn core.Connection, ids []int, lines int) (map[int]core.LogResult, error) {
	if lines < 1 {
		return nil, fmt.Errorf("line count must be positive")
	}
	unique := []int{}
	seen := map[int]bool{}
	for _, id := range ids {
		if id < 0 {
			return nil, fmt.Errorf("task IDs cannot be negative")
		}
		if !seen[id] {
			unique = append(unique, id)
			seen[id] = true
		}
	}
	result := map[int]core.LogResult{}
	for start := 0; start < len(unique); start += 16 {
		stop := min(start+16, len(unique))
		batch := unique[start:stop]
		entries, err := c.logBatch(ctx, conn, batch, lines)
		if err != nil {
			if !unavailableLog(err) {
				return result, err
			}
			if len(batch) == 1 {
				result[batch[0]] = core.LogResult{Error: err.Error(), ObservedAt: time.Now()}
				continue
			}
			for _, id := range batch {
				one, oneErr := c.logBatch(ctx, conn, []int{id}, lines)
				if oneErr != nil {
					if !unavailableLog(oneErr) {
						return result, oneErr
					}
					result[id] = core.LogResult{Error: oneErr.Error(), ObservedAt: time.Now()}
				} else {
					result[id] = one[id]
				}
			}
		} else {
			for id, value := range entries {
				result[id] = value
			}
		}
	}
	return result, nil
}

func unavailableLog(err error) bool {
	var e *Error
	if !errors.As(err, &e) {
		return false
	}
	if e.Kind == "log-unavailable" {
		return true
	}
	if e.Kind != "command" {
		return false
	}
	for _, text := range []string{"Failed reading process output file", "Failed to get log file handle", "Failed to decompress remote log output"} {
		if strings.Contains(e.Detail, text) {
			return true
		}
	}
	return false
}

func (c *Client) logBatch(ctx context.Context, conn core.Connection, ids []int, lines int) (map[int]core.LogResult, error) {
	args := []string{"log", "--json", "--lines", strconv.Itoa(lines)}
	for _, id := range ids {
		args = append(args, strconv.Itoa(id))
	}
	data, err := c.run(ctx, conn, args, false, false)
	if err != nil {
		return nil, err
	}
	var raw map[string]struct {
		Task   *wireTask `json:"task"`
		Output string    `json:"output"`
	}
	if err = json.Unmarshal(data, &raw); err != nil {
		return nil, fmt.Errorf("invalid Pueue log JSON: %w", err)
	}
	observed := time.Now()
	results := map[int]core.LogResult{}
	for _, id := range ids {
		item, exists := raw[strconv.Itoa(id)]
		value := core.LogResult{ObservedAt: observed}
		if !exists {
			value.Error = fmt.Sprintf("task #%d no longer exists", id)
			results[id] = value
			continue
		}
		if item.Task != nil {
			r := item.Task
			if r.ID != id {
				return nil, fmt.Errorf("Pueue returned inconsistent log task ID")
			}
			task := core.Task{ID: r.ID, Command: r.Command, OriginalCommand: r.OriginalCommand, Label: r.Label, Group: r.Group, Path: r.Path, Priority: r.Priority, Dependencies: r.Dependencies, CreatedAt: r.CreatedAt}
			if err := normalizeStatus(&task, r.Status, 0); err != nil {
				return nil, err
			}
			value.Task = &task
		}
		if strings.HasPrefix(strings.TrimSpace(item.Output), "(Pueue error)") {
			value.Error = "Task log is unavailable; it may not have started or its log was removed"
		} else {
			value.Output = item.Output
			if len(value.Output) > 2<<20 {
				value.Output = "…earlier output trimmed…\n" + strings.ToValidUTF8(value.Output[len(value.Output)-(2<<20):], "")
			}
		}
		results[id] = value
	}
	return results, nil
}

// FullLogCommand streams plain output. It never requests JSON/full buffering in
// this process. The caller owns terminal/pager handoff and cancellation.
func (c *Client) FullLogCommand(ctx context.Context, conn core.Connection, id int) (*exec.Cmd, error) {
	if id < 0 {
		return nil, fmt.Errorf("task ID must be nonnegative")
	}
	return c.command(ctx, conn, []string{"log", "--full", strconv.Itoa(id)}, false)
}

func (c *Client) FullLog(ctx context.Context, conn core.Connection, id int, writer io.Writer) error {
	cmd, err := c.FullLogCommand(ctx, conn, id)
	if err != nil {
		return err
	}
	stderr := &boundedBuffer{limit: 8192}
	cmd.Stdout = writer
	cmd.Stderr = stderr
	if err = cmd.Start(); err != nil {
		return classify(err, stderr.String(), ctx, false, false)
	}
	if err = cmd.Wait(); err != nil {
		return classify(err, stderr.String(), ctx, true, false)
	}
	return nil
}
