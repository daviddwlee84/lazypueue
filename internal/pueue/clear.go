package pueue

import (
	"context"
	"fmt"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/daviddwlee84/lazypueue/internal/core"
)

func (c *Client) reconcileRemoval(ctx context.Context, conn core.Connection, ids []int, guards map[int]time.Time) ([]core.Outcome, error) {
	snapshot, err := c.Snapshot(ctx, conn)
	if err != nil {
		return nil, err
	}
	outcomes := make([]core.Outcome, 0, len(ids))
	for _, id := range ids {
		outcome := core.Outcome{ID: id}
		if t, exists := taskAt(snapshot, id); exists {
			if stamp, guarded := guards[id]; !guarded || stamp.Equal(t.CreatedAt) {
				outcome.Error = "task remains; it may have changed state or still be required by another task"
			}
		}
		outcomes = append(outcomes, outcome)
	}
	return outcomes, nil
}

func outcomeError(outcomes []core.Outcome) error {
	failed := []string{}
	for _, o := range outcomes {
		if o.Error != "" {
			failed = append(failed, strconv.Itoa(o.ID))
		}
	}
	if len(failed) > 0 {
		return fmt.Errorf("operation incomplete for task IDs %s; inspect the per-task results", strings.Join(failed, ", "))
	}
	return nil
}

// removalLayers removes selected dependants before their parents. Dependencies
// outside this captured set remain untouched; Pueue protects those parents.
func removalLayers(tasks []core.Task) ([][]int, error) {
	pending := map[int]core.Task{}
	for _, t := range tasks {
		pending[t.ID] = t
	}
	layers := [][]int{}
	for len(pending) > 0 {
		parents := map[int]bool{}
		for _, t := range pending {
			for _, id := range t.Dependencies {
				if _, ok := pending[id]; ok {
					parents[id] = true
				}
			}
		}
		layer := []int{}
		for id := range pending {
			if !parents[id] {
				layer = append(layer, id)
			}
		}
		if len(layer) == 0 {
			return nil, fmt.Errorf("selected tasks contain a dependency cycle; nothing was removed")
		}
		sort.Ints(layer)
		layers = append(layers, layer)
		for _, id := range layer {
			delete(pending, id)
		}
	}
	return layers, nil
}

func (c *Client) clearGroup(ctx context.Context, conn core.Connection, req core.Request, before core.Snapshot) (core.Result, error) {
	result := core.Result{ConnectionID: conn.ID, Outcomes: []core.Outcome{}}
	var group core.Group
	found := false
	for _, g := range before.Groups {
		if g.Name == req.Group {
			group = g
			found = true
		}
	}
	if !found {
		return result, fmt.Errorf("group %q no longer exists", req.Group)
	}
	if group.Status != "running" && group.Status != "paused" {
		return result, fmt.Errorf("group is already being reset")
	}
	if req.IDs == nil {
		for _, t := range before.Tasks {
			if t.Group == req.Group {
				req.IDs = append(req.IDs, t.ID)
			}
		}
	}
	if len(req.IDs) == 0 {
		result.Message = "No tasks were captured to clear"
		return result, nil
	}
	captured := map[int]core.Task{}
	for _, id := range req.IDs {
		t, ok := taskAt(before, id)
		if !ok || t.Group != req.Group {
			return result, fmt.Errorf("task #%d left the reviewed group; nothing was cleared", id)
		}
		if t.Locked {
			return result, fmt.Errorf("task #%d is locked by an editor; finish or recover it before clearing this group", id)
		}
		if !core.Eligible("clear-group", t) {
			return result, fmt.Errorf("task #%d has an unsupported state; nothing was cleared", id)
		}
		captured[id] = t
	}
	guards := map[int]time.Time{}
	for id, t := range captured {
		guards[id] = t.CreatedAt
	}
	if _, err := removalLayers(mapTasks(captured)); err != nil {
		return result, err
	}
	pausedByUs := group.Status == "running"
	if pausedByUs {
		if _, err := c.run(ctx, conn, []string{"pause", "--wait", "--group=" + req.Group}, true, false); err != nil {
			return markError(result, err)
		}
	}
	// From here, every failure leaves scheduling paused. Starting a partially
	// cleared queue would execute work the user explicitly wanted removed.
	fail := func(err error) (core.Result, error) {
		result.Message = "Clear incomplete; group " + req.Group + " remains paused. " + err.Error()
		return markError(result, fmt.Errorf("clear incomplete; group %s remains paused: %w", req.Group, err))
	}
	fresh, err := c.Snapshot(ctx, conn)
	if err != nil {
		return fail(err)
	}
	active := []string{}
	for id, expected := range captured {
		t, ok := taskAt(fresh, id)
		if !ok {
			continue
		}
		if !t.CreatedAt.Equal(expected.CreatedAt) {
			return fail(fmt.Errorf("task #%d was replaced; no replacement will be stopped", id))
		}
		if t.State() == "running" || t.State() == "paused" {
			active = append(active, strconv.Itoa(id))
		}
	}
	if len(active) > 0 {
		if _, err = c.run(ctx, conn, append([]string{"kill"}, active...), true, false); err != nil {
			return fail(err)
		}
	}
	deadline := time.Now().Add(6 * time.Second)
	for {
		fresh, err = c.Snapshot(ctx, conn)
		if err != nil {
			return fail(err)
		}
		live := false
		for id, old := range captured {
			if t, ok := taskAt(fresh, id); ok && t.CreatedAt.Equal(old.CreatedAt) && (t.State() == "running" || t.State() == "paused") {
				live = true
			}
		}
		if !live {
			break
		}
		if time.Now().After(deadline) {
			return fail(fmt.Errorf("some captured processes have not stopped; no logs were removed"))
		}
		select {
		case <-ctx.Done():
			return fail(ctx.Err())
		case <-time.After(100 * time.Millisecond):
		}
	}
	removable := []core.Task{}
	for id, old := range captured {
		if t, ok := taskAt(fresh, id); ok && t.CreatedAt.Equal(old.CreatedAt) && !t.Locked {
			removable = append(removable, t)
		}
	}
	layers, err := removalLayers(removable)
	if err != nil {
		return fail(err)
	}
	for _, ids := range layers {
		current, readErr := c.Snapshot(ctx, conn)
		if readErr != nil {
			return fail(readErr)
		}
		args := []string{"remove"}
		for _, id := range ids {
			t, exists := taskAt(current, id)
			if !exists {
				continue
			}
			if !t.CreatedAt.Equal(guards[id]) {
				return fail(fmt.Errorf("task #%d was replaced; removal stopped", id))
			}
			if !core.Eligible("remove", t) {
				return fail(fmt.Errorf("task #%d is no longer removable", id))
			}
			args = append(args, strconv.Itoa(id))
		}
		if len(args) == 1 {
			continue
		}
		_, removeErr := c.run(ctx, conn, args, true, false)
		if removeErr != nil {
			result, err = markError(result, removeErr)
			if result.Unknown {
				return fail(err)
			}
			// A definite daemon refusal can be dependency protection. Reconcile below
			// and continue independent leaves rather than cascading outside the group.
		}
	}
	result.Outcomes, err = c.reconcileRemoval(ctx, conn, req.IDs, guards)
	if err != nil {
		return fail(err)
	}
	if err = outcomeError(result.Outcomes); err != nil {
		return fail(err)
	}
	if pausedByUs {
		final, err := c.Snapshot(ctx, conn)
		if err != nil {
			return fail(err)
		}
		for _, t := range final.Tasks {
			if t.Group == req.Group && t.State() == "paused" {
				return fail(fmt.Errorf("an unreviewed paused task appeared; group scheduling was not resumed"))
			}
		}
		if _, err = c.run(ctx, conn, []string{"start", "--group=" + req.Group}, true, false); err != nil {
			return fail(err)
		}
	}
	result.Message = fmt.Sprintf("Cleared %d reviewed tasks and their logs from %s; group remains %s", len(captured), req.Group, group.Status)
	return result, nil
}

func mapTasks(m map[int]core.Task) []core.Task {
	out := make([]core.Task, 0, len(m))
	for _, t := range m {
		out = append(out, t)
	}
	return out
}
