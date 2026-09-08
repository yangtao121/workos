package orchestration

import (
	"context"
	"errors"
	"time"

	agentv1 "github.com/yangtao121/workos/gen/go/workos/agent/v1"
	agentdomain "github.com/yangtao121/workos/internal/core/agent/domain"
	agentports "github.com/yangtao121/workos/internal/core/agent/ports"
	registryapp "github.com/yangtao121/workos/internal/core/appregistry/application"
	registrydomain "github.com/yangtao121/workos/internal/core/appregistry/domain"
	"github.com/yangtao121/workos/internal/platform/dbtx"
	"google.golang.org/protobuf/encoding/protojson"
)

type RepairSourceLeaseAuthority interface {
	LockTaskArtifactStream(context.Context, dbtx.Tx, string, string, time.Time) (agentports.TaskStreamFacts, error)
	TaskLeaseExpiry(context.Context, dbtx.Tx, string, string, time.Time) (time.Time, bool, error)
}

type RepairBuildInput struct {
	TaskID string
	Target *agentv1.RepairTarget
	Build  registryapp.BuildInput
}

type RepairSources struct {
	pool   TaskTxSource
	leases RepairSourceLeaseAuthority
	builds *registryapp.BuildService
}

func NewRepairSources(pool TaskTxSource, leases RepairSourceLeaseAuthority, builds *registryapp.BuildService) (*RepairSources, error) {
	if pool == nil || leases == nil || builds == nil {
		return nil, errors.New("repair sources require transactions, task authority and registry")
	}
	return &RepairSources{pool: pool, leases: leases, builds: builds}, nil
}

func (s *RepairSources) Resolve(ctx context.Context, leaseID, workerID string) (RepairBuildInput, error) {
	input, _, err := s.execute(ctx, leaseID, workerID, nil, false)
	return input, err
}
func (s *RepairSources) Submit(ctx context.Context, leaseID, workerID string, files []registrydomain.SourceFile) (string, registrydomain.SourceBundle, error) {
	input, source, err := s.execute(ctx, leaseID, workerID, files, true)
	return input.TaskID, source, err
}
func (s *RepairSources) execute(ctx context.Context, leaseID, workerID string, files []registrydomain.SourceFile, submit bool) (RepairBuildInput, registrydomain.SourceBundle, error) {
	fail := func(err error) (RepairBuildInput, registrydomain.SourceBundle, error) {
		return RepairBuildInput{}, registrydomain.SourceBundle{}, err
	}
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return fail(storeFailureContext("begin repair source", err))
	}
	defer tx.Rollback(ctx) //nolint:errcheck
	stream, err := s.leases.LockTaskArtifactStream(ctx, tx, leaseID, workerID, time.Now().UTC())
	if err != nil {
		return fail(err)
	}
	if stream.CancellationRequested {
		return fail(agentdomain.ErrLeaseLost)
	}
	input := &agentv1.AgentTaskInput{}
	if protojson.Unmarshal(stream.Input, input) != nil || !validRepairSourceTask(stream, input) {
		return fail(agentdomain.ErrInvalid)
	}
	target := input.GetRepairTarget()
	build, err := s.builds.Resolve(ctx, tx, stream.OwnerUserID, target.GetAppId(), target.GetVersion(), target.GetManifestDigest())
	if err != nil {
		return fail(err)
	}
	var source registrydomain.SourceBundle
	if submit {
		source, err = s.builds.SubmitSource(ctx, tx, stream.OwnerUserID, stream.TaskID, files)
		if err != nil {
			return fail(err)
		}
	}
	// Recheck after lock waits and Registry work. A lease that expired while
	// this transaction waited may neither reveal input nor commit a candidate.
	expiry, live, err := s.leases.TaskLeaseExpiry(ctx, tx, leaseID, workerID, time.Now().UTC())
	if err != nil {
		return fail(err)
	}
	if !live || !expiry.After(time.Now().UTC()) {
		return fail(agentdomain.ErrLeaseLost)
	}
	if err := tx.Commit(ctx); err != nil {
		return fail(storeFailureContext("commit repair source", err))
	}
	return RepairBuildInput{TaskID: stream.TaskID, Target: target, Build: build}, source, nil
}
func validRepairSourceTask(stream agentports.TaskStreamFacts, input *agentv1.AgentTaskInput) bool {
	target := input.GetRepairTarget()
	if !agentdomain.ValidAppTaskUUID(stream.TaskID) || !agentdomain.ValidAppTaskUUID(stream.OwnerUserID) || !agentdomain.ValidAppTaskUUID(stream.ProjectID) || input.GetTargetScope().GetProjectId() != stream.ProjectID || !agentdomain.ValidAppTaskUUID(input.GetIncidentId()) || !agentdomain.ValidAppTaskUUID(target.GetAppInstanceId()) || !registrydomain.ValidAppID(target.GetAppId()) || !registrydomain.ValidWebBundleArtifactDigest(target.GetManifestDigest()) || target.GetProjectRevision() <= 0 {
		return false
	}
	_, ok := registrydomain.ParseVersion(target.GetVersion())
	return ok
}
