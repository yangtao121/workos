// Package transport exposes the public IncidentService over Connect and
// adapts the runtime's private SupervisedWorkloadService to the Reliability
// module's neutral observer/control ports. The incident handler is
// identity-protected and owner-scoped: the owner comes exclusively from the
// trusted gateway-injected identity, never from the request body.
package transport

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"time"

	"connectrpc.com/connect"
	"google.golang.org/protobuf/types/known/timestamppb"

	commonv1 "github.com/yangtao121/workos/gen/go/workos/common/v1"
	incidentv1 "github.com/yangtao121/workos/gen/go/workos/incident/v1"
	incidentv1connect "github.com/yangtao121/workos/gen/go/workos/incident/v1/incidentv1connect"
	workloadv1 "github.com/yangtao121/workos/gen/go/workos/workload/v1"
	workloadv1connect "github.com/yangtao121/workos/gen/go/workos/workload/v1/workloadv1connect"
	"github.com/yangtao121/workos/internal/platform/identity"
	"github.com/yangtao121/workos/internal/reliability/application"
	"github.com/yangtao121/workos/internal/reliability/domain"
	"github.com/yangtao121/workos/internal/reliability/ports"
)

// IncidentHandler exposes the public IncidentService.
type IncidentHandler struct {
	service *application.IncidentService
}

// NewIncidentConnectHandler wires the public transport.
func NewIncidentConnectHandler(service *application.IncidentService) (string, http.Handler) {
	return incidentv1connect.NewIncidentServiceHandler(&IncidentHandler{service: service})
}

func (h *IncidentHandler) GetIncident(ctx context.Context, req *connect.Request[incidentv1.GetIncidentRequest]) (*connect.Response[incidentv1.GetIncidentResponse], error) {
	id, err := identity.FromContext(ctx)
	if err != nil {
		return nil, connect.NewError(connect.CodeUnauthenticated, err)
	}
	incident, err := h.service.Get(ctx, id.UserID, req.Msg.GetIncidentId())
	if err != nil {
		return nil, mapError(err)
	}
	return connect.NewResponse(&incidentv1.GetIncidentResponse{Incident: incidentToProto(incident)}), nil
}

func (h *IncidentHandler) ListIncidents(ctx context.Context, req *connect.Request[incidentv1.ListIncidentsRequest]) (*connect.Response[incidentv1.ListIncidentsResponse], error) {
	id, err := identity.FromContext(ctx)
	if err != nil {
		return nil, connect.NewError(connect.CodeUnauthenticated, err)
	}
	pageSize := int(req.Msg.GetPage().GetPageSize())
	if req.Msg.GetPage() != nil && req.Msg.GetPage().GetPageSize() < 0 {
		return nil, connect.NewError(connect.CodeInvalidArgument, errors.New("incident list request is invalid"))
	}
	incidents, next, err := h.service.List(ctx, id.UserID, req.Msg.GetProjectId(), pageSize, req.Msg.GetPage().GetPageToken())
	if err != nil {
		return nil, mapError(err)
	}
	items := make([]*incidentv1.Incident, 0, len(incidents))
	for _, incident := range incidents {
		items = append(items, incidentToProto(incident))
	}
	return connect.NewResponse(&incidentv1.ListIncidentsResponse{
		Incidents: items,
		Page:      &commonv1.PageResponse{NextPageToken: next},
	}), nil
}

func (h *IncidentHandler) AcknowledgeIncident(ctx context.Context, req *connect.Request[incidentv1.AcknowledgeIncidentRequest]) (*connect.Response[incidentv1.AcknowledgeIncidentResponse], error) {
	id, err := identity.FromContext(ctx)
	if err != nil {
		return nil, connect.NewError(connect.CodeUnauthenticated, err)
	}
	incident, err := h.service.Acknowledge(ctx, id.UserID, req.Msg.GetIncidentId(), req.Msg.GetIdempotencyKey())
	if err != nil {
		return nil, mapError(err)
	}
	return connect.NewResponse(&incidentv1.AcknowledgeIncidentResponse{Incident: incidentToProto(incident)}), nil
}

// mapError converts incident verdicts to sanitized Connect codes: no SQL,
// constraint names, workload internals, or evidence content.
func mapError(err error) error {
	switch {
	case err == nil:
		return nil
	case errors.Is(err, domain.ErrInvalid):
		return connect.NewError(connect.CodeInvalidArgument, errors.New("incident request is invalid"))
	case errors.Is(err, domain.ErrNotFound):
		return connect.NewError(connect.CodeNotFound, errors.New("incident is not available"))
	case errors.Is(err, domain.ErrIdempotencyConflict):
		return connect.NewError(connect.CodeAborted, errors.New("acknowledge key was already used for a different incident"))
	case errors.Is(err, domain.ErrUnavailable):
		return connect.NewError(connect.CodeUnavailable, errors.New("incident store is temporarily unavailable"))
	default:
		return connect.NewError(connect.CodeInternal, errors.New("incident operation failed"))
	}
}

func incidentToProto(incident domain.Incident) *incidentv1.Incident {
	proto := &incidentv1.Incident{
		Id: incident.ID, WorkloadId: incident.WorkloadID, ProjectId: incident.ProjectID,
		Severity: severityProto(incident.Violation.Severity()),
		State:    stateProto(incident.State),
		Summary:  incident.Summary,
		Evidence: []*incidentv1.EvidenceRef{{
			Type: "observation", Digest: incident.EvidenceDigest,
		}},
		CreatedAt:          timestamp(incident.CreatedAt),
		UpdatedAt:          timestamp(incident.UpdatedAt),
		OwnerUserId:        incident.OwnerUserID,
		AppInstanceId:      incident.AppInstanceID,
		AppId:              incident.AppID,
		Violation:          violationProto(incident.Violation),
		WorkloadGeneration: incident.WorkloadGeneration,
		Revision:           incident.Revision,
		RestartOutcome:     outcomeProto(incident.RestartOutcome),
	}
	if incident.AcknowledgedAt != nil {
		proto.AcknowledgedAt = timestamp(*incident.AcknowledgedAt)
	}
	if incident.MitigatedAt != nil {
		proto.MitigatedAt = timestamp(*incident.MitigatedAt)
	}
	if incident.ResolvedAt != nil {
		proto.ResolvedAt = timestamp(*incident.ResolvedAt)
	}
	return proto
}

func severityProto(severity domain.Severity) incidentv1.IncidentSeverity {
	switch severity {
	case domain.SeverityInfo:
		return incidentv1.IncidentSeverity_INCIDENT_SEVERITY_INFO
	case domain.SeverityWarning:
		return incidentv1.IncidentSeverity_INCIDENT_SEVERITY_WARNING
	case domain.SeverityCritical:
		return incidentv1.IncidentSeverity_INCIDENT_SEVERITY_CRITICAL
	default:
		return incidentv1.IncidentSeverity_INCIDENT_SEVERITY_UNSPECIFIED
	}
}

func stateProto(state domain.State) incidentv1.IncidentState {
	switch state {
	case domain.StateOpen:
		return incidentv1.IncidentState_INCIDENT_STATE_OPEN
	case domain.StateMitigated:
		return incidentv1.IncidentState_INCIDENT_STATE_MITIGATED
	case domain.StateResolved:
		return incidentv1.IncidentState_INCIDENT_STATE_RESOLVED
	default:
		return incidentv1.IncidentState_INCIDENT_STATE_UNSPECIFIED
	}
}

func violationProto(violation domain.Violation) incidentv1.IncidentViolation {
	switch violation {
	case domain.ViolationUnexpectedExit:
		return incidentv1.IncidentViolation_INCIDENT_VIOLATION_UNEXPECTED_EXIT
	case domain.ViolationHealthFailure:
		return incidentv1.IncidentViolation_INCIDENT_VIOLATION_HEALTH_FAILURE
	case domain.ViolationOOM:
		return incidentv1.IncidentViolation_INCIDENT_VIOLATION_OOM
	case domain.ViolationPIDsLimit:
		return incidentv1.IncidentViolation_INCIDENT_VIOLATION_PIDS_LIMIT
	case domain.ViolationRestartLimit:
		return incidentv1.IncidentViolation_INCIDENT_VIOLATION_RESTART_LIMIT_EXHAUSTED
	default:
		return incidentv1.IncidentViolation_INCIDENT_VIOLATION_UNSPECIFIED
	}
}

func outcomeProto(outcome domain.RestartOutcome) incidentv1.IncidentRestartOutcome {
	switch outcome {
	case domain.OutcomePending:
		return incidentv1.IncidentRestartOutcome_INCIDENT_RESTART_OUTCOME_PENDING
	case domain.OutcomeRestarted:
		return incidentv1.IncidentRestartOutcome_INCIDENT_RESTART_OUTCOME_RESTARTED
	case domain.OutcomeStopped:
		return incidentv1.IncidentRestartOutcome_INCIDENT_RESTART_OUTCOME_STOPPED
	case domain.OutcomeFailed:
		return incidentv1.IncidentRestartOutcome_INCIDENT_RESTART_OUTCOME_FAILED
	default:
		return incidentv1.IncidentRestartOutcome_INCIDENT_RESTART_OUTCOME_UNSPECIFIED
	}
}

func timestamp(value time.Time) *timestamppb.Timestamp { return timestamppb.New(value) }

// ---------------------------------------------------------------------------
// Runtime client adapter: the private SupervisedWorkloadService satisfies
// the neutral observer/control ports. Verdicts fold into sanitized control
// outcomes; the adapter never imports the runtime module.
// ---------------------------------------------------------------------------

// RuntimeClient adapts the runtime's private supervised workload service.
type RuntimeClient struct {
	client workloadv1connect.SupervisedWorkloadServiceClient
}

func NewRuntimeClient(client workloadv1connect.SupervisedWorkloadServiceClient) (*RuntimeClient, error) {
	if client == nil {
		return nil, errors.New("reliability runtime client requires the supervised workload client")
	}
	return &RuntimeClient{client: client}, nil
}

func (c *RuntimeClient) ListObservations(ctx context.Context) ([]ports.Observation, error) {
	response, err := c.client.ListObservations(ctx, connect.NewRequest(&workloadv1.ListObservationsRequest{}))
	if err != nil {
		return nil, fmt.Errorf("%w: %s", ports.ErrRuntimeUnavailable, connect.CodeOf(err).String())
	}
	observations := make([]ports.Observation, 0, len(response.Msg.GetObservations()))
	for _, item := range response.Msg.GetObservations() {
		observedAt, _ := time.Parse(time.RFC3339Nano, item.GetObservedAt())
		if observedAt.IsZero() {
			observedAt = time.Now().UTC()
		}
		pidsLimitEvents := item.GetPidsEventsMax()
		if pidsLimitEvents == 0 {
			// Rolling-upgrade compatibility with runtime versions that emitted
			// the same pids.events `max` value under the legacy field name.
			pidsLimitEvents = item.GetPidsEventsPeak()
		}
		observations = append(observations, ports.Observation{
			WorkloadID: item.GetWorkloadId(), OwnerUserID: item.GetOwnerUserId(),
			ProjectID: item.GetProjectId(), AppInstanceID: item.GetAppInstanceId(),
			AppID: item.GetAppId(), ManifestDigest: item.GetManifestDigest(),
			Generation: item.GetGeneration(), State: stateFromProto(item.GetState()),
			RestartCount:  int64(item.GetRestartCount()),
			HealthVerdict: item.GetHealthVerdict(), ExitCategory: item.GetExitCategory(),
			Idle: item.GetIdle(), MemoryOOMs: item.GetMemoryEventsOom(),
			PIDsLimitEvents: pidsLimitEvents, ObservedAt: observedAt,
		})
	}
	return observations, nil
}

func (c *RuntimeClient) Restart(ctx context.Context, workloadID, actionKey string) (ports.ControlResult, error) {
	response, err := c.client.RestartWorkload(ctx, connect.NewRequest(&workloadv1.RestartWorkloadRequest{
		WorkloadId: workloadID, ActionKey: actionKey,
	}))
	if err != nil {
		return controlError(err)
	}
	return ports.ControlResult{Outcome: ports.ControlRestarted, Generation: response.Msg.GetGeneration()}, nil
}

func (c *RuntimeClient) Stop(ctx context.Context, workloadID, actionKey, reason string) (ports.ControlResult, error) {
	_, err := c.client.TerminateWorkload(ctx, connect.NewRequest(&workloadv1.TerminateWorkloadRequest{
		WorkloadId: workloadID, ActionKey: actionKey, Reason: reason,
	}))
	if err != nil {
		return controlError(err)
	}
	return ports.ControlResult{Outcome: ports.ControlStopped}, nil
}

// controlError folds sanitized Connect refusals into bounded control
// outcomes. Every failure is a decision input, never raw engine detail.
func controlError(err error) (ports.ControlResult, error) {
	switch connect.CodeOf(err) {
	case connect.CodeResourceExhausted:
		return ports.ControlResult{Outcome: ports.ControlLimitExhausted}, nil
	case connect.CodeFailedPrecondition:
		return ports.ControlResult{Outcome: ports.ControlUnsupported}, nil
	case connect.CodeNotFound:
		return ports.ControlResult{Outcome: ports.ControlUnsupported}, nil
	case connect.CodeAborted:
		return ports.ControlResult{Outcome: ports.ControlConflict}, nil
	case connect.CodeUnavailable, connect.CodeDeadlineExceeded, connect.CodeCanceled:
		return ports.ControlResult{Outcome: ports.ControlUnavailable}, nil
	default:
		return ports.ControlResult{Outcome: ports.ControlFailed}, nil
	}
}

func stateFromProto(state workloadv1.SupervisedWorkloadState) ports.WorkloadState {
	switch state {
	case workloadv1.SupervisedWorkloadState_SUPERVISED_WORKLOAD_STATE_PENDING:
		return ports.StatePending
	case workloadv1.SupervisedWorkloadState_SUPERVISED_WORKLOAD_STATE_STARTING:
		return ports.StateStarting
	case workloadv1.SupervisedWorkloadState_SUPERVISED_WORKLOAD_STATE_RUNNING:
		return ports.StateRunning
	case workloadv1.SupervisedWorkloadState_SUPERVISED_WORKLOAD_STATE_STOPPING:
		return ports.StateStopping
	case workloadv1.SupervisedWorkloadState_SUPERVISED_WORKLOAD_STATE_STOPPED:
		return ports.StateStopped
	case workloadv1.SupervisedWorkloadState_SUPERVISED_WORKLOAD_STATE_FAILED:
		return ports.StateFailed
	default:
		return ports.StateUnknown
	}
}
