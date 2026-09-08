// Repair orchestrator (ADR-0016 §5): turns reliability incidents into
// ordinary queued Agent repair tasks. One repair task per incident, durable
// idempotency through the reliability ledger plus the Agent idempotency
// mapping, routing left to the standard Task Router admission chain (project
// binding provider, credential snapshots, budget and queue discipline).
package application

import (
	"context"
	"fmt"
)

// RepairCandidate is one open incident eligible for a repair task.
type RepairCandidate struct {
	IncidentID    string
	OwnerUserID   string
	ProjectID     string
	AppInstanceID string
	Summary       string
}

// RepairCandidatesSource reads repair-eligible incidents and records the
// submitted task.
type RepairCandidatesSource interface {
	// ListRepairCandidates returns open incidents without a ledger row,
	// oldest first, bounded by limit.
	ListRepairCandidates(ctx context.Context, limit int) ([]RepairCandidate, error)
	// RecordRepairSubmitted inserts the ledger row (idempotent on the
	// incident id) and projects the repair task id onto the incident.
	RecordRepairSubmitted(ctx context.Context, candidate RepairCandidate, taskID string) error
	// ListRepairCompleted rotates through submitted rows by their last poll.
	// Core remains the authority for the task's actual terminal state.
	ListRepairCompleted(ctx context.Context, limit int) ([]RepairCompletedRow, error)
	// ClearRepairCompleted moves the row out of the submitted state after
	// the hand-off consumed it.
	ClearRepairCompleted(ctx context.Context, incidentID string) error
}

// RepairCompletedRow is a submitted ledger row awaiting reconciliation.
type RepairCompletedRow struct {
	RepairCandidate
	TaskID string
}

type RepairTaskState int

const (
	RepairTaskPending RepairTaskState = iota
	RepairTaskCompleted
	RepairTaskFailed
)

// RepairSubmitter admits the repair task on Core through the private repair
// RPC. The deterministic key makes retries and crash replays resolve to the
// same task.
type RepairSubmitter interface {
	SubmitRepair(ctx context.Context, ownerUserID, projectID, appInstanceID, incidentID, idempotencyKey, violationSummary string) (taskID, providerID string, err error)
	// TaskState verifies task provenance and reads Core's terminal state.
	TaskState(ctx context.Context, row RepairCompletedRow) (RepairTaskState, error)
}

// RepairCompletionHandler receives incidents whose repair task reached the
// terminal completed state — the deployment controller's canary trigger.
type RepairCompletionHandler interface {
	HandleRepairCompleted(ctx context.Context, row RepairCompletedRow) error
}

// RepairOrchestrator drives the bounded repair pass and the repair-to-
// deployment hand-off.
type RepairOrchestrator struct {
	candidates RepairCandidatesSource
	submitter  RepairSubmitter
	completion RepairCompletionHandler
}

func NewRepairOrchestrator(candidates RepairCandidatesSource, submitter RepairSubmitter, completion RepairCompletionHandler) (*RepairOrchestrator, error) {
	if candidates == nil || submitter == nil {
		return nil, fmt.Errorf("repair orchestrator requires candidates source and submitter")
	}
	return &RepairOrchestrator{candidates: candidates, submitter: submitter, completion: completion}, nil
}

// RunPass performs one bounded repair pass: every incident without a
// ledger row gets exactly one repair task submitted. Returns the number of
// repair tasks submitted and the last per-candidate submission error (one
// failing incident never blocks the rest of the pass).
func (o *RepairOrchestrator) RunPass(ctx context.Context, limit int) (int, error) {
	candidates, err := o.candidates.ListRepairCandidates(ctx, limit)
	if err != nil {
		return 0, err
	}
	submitted := 0
	var lastErr error
	for _, candidate := range candidates {
		key := "repair-" + candidate.IncidentID
		taskID, _, err := o.submitter.SubmitRepair(ctx, candidate.OwnerUserID, candidate.ProjectID, candidate.AppInstanceID, candidate.IncidentID, key, candidate.Summary)
		if err != nil {
			lastErr = err
			continue
		}
		if err := o.candidates.RecordRepairSubmitted(ctx, candidate, taskID); err != nil {
			return submitted, err
		}
		submitted++
	}

	// Repair-to-deployment hand-off (ADR-0016 §6): submitted ledger rows
	// whose repair task completed on Core trigger the deployment controller's
	// canary. The candidates source reports terminal completions.
	if o.completion != nil {
		completed, completedErr := o.candidates.ListRepairCompleted(ctx, 4)
		if completedErr != nil {
			return submitted, completedErr
		}
		for _, row := range completed {
			state, doneErr := o.submitter.TaskState(ctx, row)
			if doneErr != nil {
				lastErr = doneErr
				continue
			}
			switch state {
			case RepairTaskCompleted:
				if err := o.completion.HandleRepairCompleted(ctx, row); err != nil {
					lastErr = err
					continue
				}
			case RepairTaskFailed:
				// Failed/cancelled tasks have no deployable output. Preserve
				// the task link but stop polling the immutable terminal fact.
			case RepairTaskPending:
				continue
			default:
				lastErr = fmt.Errorf("invalid repair task state")
				continue
			}
			if err := o.candidates.ClearRepairCompleted(ctx, row.IncidentID); err != nil {
				return submitted, err
			}
		}
	}
	return submitted, lastErr
}
