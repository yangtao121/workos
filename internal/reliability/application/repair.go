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
	IncidentID  string
	OwnerUserID string
	ProjectID   string
	Summary     string
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
}

// RepairSubmitter admits the repair task on Core through the private repair
// RPC. The deterministic key makes retries and crash replays resolve to the
// same task.
type RepairSubmitter interface {
	SubmitRepair(ctx context.Context, ownerUserID, projectID, incidentID, idempotencyKey, violationSummary string) (taskID, providerID string, err error)
}

// RepairOrchestrator drives the bounded repair pass.
type RepairOrchestrator struct {
	candidates RepairCandidatesSource
	submitter  RepairSubmitter
}

func NewRepairOrchestrator(candidates RepairCandidatesSource, submitter RepairSubmitter) (*RepairOrchestrator, error) {
	if candidates == nil || submitter == nil {
		return nil, fmt.Errorf("repair orchestrator requires candidates source and submitter")
	}
	return &RepairOrchestrator{candidates: candidates, submitter: submitter}, nil
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
		taskID, _, err := o.submitter.SubmitRepair(ctx, candidate.OwnerUserID, candidate.ProjectID, candidate.IncidentID, key, candidate.Summary)
		if err != nil {
			lastErr = err
			continue
		}
		if err := o.candidates.RecordRepairSubmitted(ctx, candidate, taskID); err != nil {
			return submitted, err
		}
		submitted++
	}
	return submitted, lastErr
}
