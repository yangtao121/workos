package orchestration

import (
	"context"
	"errors"
	"strings"

	agentv1 "github.com/yangtao121/workos/gen/go/workos/agent/v1"
	agentapp "github.com/yangtao121/workos/internal/core/agent/application"
	agentdomain "github.com/yangtao121/workos/internal/core/agent/domain"
	agentports "github.com/yangtao121/workos/internal/core/agent/ports"
	projectdomain "github.com/yangtao121/workos/internal/core/project/domain"
	"google.golang.org/protobuf/encoding/protojson"
	"google.golang.org/protobuf/proto"
)

type RepairAdmissionInput struct {
	OwnerUserID, ProjectID, InstallationID, IncidentID, IdempotencyKey, Summary string
}

type RepairTasks interface {
	GetByIdempotency(context.Context, string, string) (agentdomain.Task, error)
}
type RepairTaskRouter interface {
	SubmitWithResult(context.Context, agentapp.SubmitInput) (agentports.TaskSubmission, error)
}
type RepairTargetSource interface {
	Get(context.Context, string, string, string) (projectdomain.RepairTarget, error)
}

type RepairAdmission struct {
	tasks   RepairTasks
	router  RepairTaskRouter
	targets RepairTargetSource
}

func NewRepairAdmission(tasks RepairTasks, router RepairTaskRouter, targets RepairTargetSource) (*RepairAdmission, error) {
	if tasks == nil || router == nil || targets == nil {
		return nil, errors.New("repair admission requires task, router and target sources")
	}
	return &RepairAdmission{tasks: tasks, router: router, targets: targets}, nil
}

func (s *RepairAdmission) Submit(ctx context.Context, input RepairAdmissionInput) (agentports.TaskSubmission, error) {
	input.Summary = strings.TrimSpace(input.Summary)
	for _, id := range []string{input.OwnerUserID, input.ProjectID, input.InstallationID, input.IncidentID} {
		if !agentdomain.ValidAppTaskUUID(id) {
			return agentports.TaskSubmission{}, agentdomain.ErrInvalid
		}
	}
	if input.IdempotencyKey == "" || len(input.IdempotencyKey) > 128 || input.Summary == "" || len(input.Summary) > 512 || strings.ContainsFunc(input.Summary, func(r rune) bool { return r < 0x20 || r == 0x7f }) {
		return agentports.TaskSubmission{}, agentdomain.ErrInvalid
	}
	stored, err := s.tasks.GetByIdempotency(ctx, input.OwnerUserID, input.IdempotencyKey)
	if err == nil {
		return repairReplay(input, stored)
	}
	if !errors.Is(err, agentdomain.ErrNotFound) {
		return agentports.TaskSubmission{}, err
	}
	target, err := s.targets.Get(ctx, input.OwnerUserID, input.ProjectID, input.InstallationID)
	if err != nil {
		return agentports.TaskSubmission{}, err
	}
	installation := target.Installation
	if installation.ID != input.InstallationID || installation.OwnerUserID != input.OwnerUserID || installation.ProjectID != input.ProjectID || installation.UninstalledAt != nil || projectdomain.ValidateStoredInstallation(installation) != nil || target.ProjectRevision <= 0 {
		return agentports.TaskSubmission{}, projectdomain.ErrInstallationCorrupt
	}
	snapshot := &agentv1.RepairTarget{AppInstanceId: installation.ID, AppId: installation.AppID, Version: installation.Version, ManifestDigest: installation.ManifestDigest, ProjectRevision: target.ProjectRevision}
	payload, err := protojson.Marshal(repairTaskInput(input, snapshot))
	if err != nil {
		return agentports.TaskSubmission{}, agentdomain.ErrInvalid
	}
	result, err := s.router.SubmitWithResult(ctx, agentapp.SubmitInput{OwnerUserID: input.OwnerUserID, ProjectID: input.ProjectID, IdempotencyKey: input.IdempotencyKey, Payload: payload})
	if errors.Is(err, agentdomain.ErrIdempotencyConflict) {
		// A concurrent admission may have snapshotted an earlier version. Only
		// matching caller facts may adopt its immutable target.
		stored, readErr := s.tasks.GetByIdempotency(ctx, input.OwnerUserID, input.IdempotencyKey)
		if readErr != nil {
			return agentports.TaskSubmission{}, readErr
		}
		return repairReplay(input, stored)
	}
	return result, err
}

func repairTaskInput(input RepairAdmissionInput, target *agentv1.RepairTarget) *agentv1.AgentTaskInput {
	return &agentv1.AgentTaskInput{TargetScope: &agentv1.TargetScope{Scope: &agentv1.TargetScope_ProjectId{ProjectId: input.ProjectID}}, Role: "general", Goal: input.Summary, IncidentId: input.IncidentID, RepairTarget: target}
}
func repairReplay(input RepairAdmissionInput, task agentdomain.Task) (agentports.TaskSubmission, error) {
	stored := &agentv1.AgentTaskInput{}
	if task.OwnerUserID != input.OwnerUserID || task.ProjectID != input.ProjectID || protojson.Unmarshal(task.Input, stored) != nil {
		return agentports.TaskSubmission{}, agentdomain.ErrIdempotencyConflict
	}
	target := stored.GetRepairTarget()
	if target.GetAppInstanceId() != input.InstallationID || !projectdomain.ValidInstallationAppID(target.GetAppId()) || !projectdomain.ValidInstallationVersion(target.GetVersion()) || !projectdomain.ValidInstallationManifestDigest(target.GetManifestDigest()) || target.GetProjectRevision() <= 0 || !proto.Equal(stored, repairTaskInput(input, target)) {
		return agentports.TaskSubmission{}, agentdomain.ErrIdempotencyConflict
	}
	return agentports.TaskSubmission{Task: task}, nil
}
