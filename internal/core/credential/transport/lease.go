package transport

import (
	"context"
	"net/http"
	"time"

	"connectrpc.com/connect"
	"google.golang.org/protobuf/types/known/timestamppb"

	credentialv1 "github.com/yangtao121/workos/gen/go/workos/credential/v1"
	"github.com/yangtao121/workos/gen/go/workos/credential/v1/credentialv1connect"
	"github.com/yangtao121/workos/internal/core/credential/ports"
)

// LeaseIssuer is the composition-provided lease coordination contract. The
// orchestration layer implements it by deriving every fact from the active
// task lease inside one controlled transaction; the transport never accepts
// owner, project, provider, credential reference, or revision from callers.
type LeaseIssuer interface {
	Acquire(ctx context.Context, taskLeaseID, workerID string) (ports.LeaseGrant, error)
	Renew(ctx context.Context, credentialLeaseID, taskLeaseID, workerID string) (ports.LeaseVerdict, error)
	Release(ctx context.Context, credentialLeaseID, taskLeaseID, workerID string) error
}

// LeaseHandler serves CredentialLeaseService on the private mTLS execution
// listener.
type LeaseHandler struct{ issuer LeaseIssuer }

func NewLeaseHandler(issuer LeaseIssuer) *LeaseHandler { return &LeaseHandler{issuer: issuer} }

func (h *LeaseHandler) AcquireTaskCredential(ctx context.Context, req *connect.Request[credentialv1.AcquireTaskCredentialRequest]) (*connect.Response[credentialv1.AcquireTaskCredentialResponse], error) {
	msg := req.Msg
	if msg.GetTaskLeaseId() == "" || msg.GetWorkerId() == "" {
		return nil, connect.NewError(connect.CodeInvalidArgument, errInvalidLeaseRequest)
	}
	grant, err := h.issuer.Acquire(ctx, msg.GetTaskLeaseId(), msg.GetWorkerId())
	if err != nil {
		return nil, mapError(err)
	}
	response := &credentialv1.AcquireTaskCredentialResponse{
		CredentialLeaseId:  grant.LeaseID,
		TaskLeaseId:        grant.TaskLeaseID,
		ConsumerId:         grant.ConsumerID,
		Purpose:            grant.Purpose,
		CredentialRevision: grant.CredentialRevision,
		ExpiresAt:          expiresProto(grant.ExpiresAt),
		Secret:             grant.Secret,
		Required:           grant.Required,
	}
	return connect.NewResponse(response), nil
}

func (h *LeaseHandler) RenewTaskCredentialLease(ctx context.Context, req *connect.Request[credentialv1.RenewTaskCredentialLeaseRequest]) (*connect.Response[credentialv1.RenewTaskCredentialLeaseResponse], error) {
	msg := req.Msg
	if msg.GetCredentialLeaseId() == "" || msg.GetTaskLeaseId() == "" || msg.GetWorkerId() == "" {
		return nil, connect.NewError(connect.CodeInvalidArgument, errInvalidLeaseRequest)
	}
	verdict, err := h.issuer.Renew(ctx, msg.GetCredentialLeaseId(), msg.GetTaskLeaseId(), msg.GetWorkerId())
	if err != nil {
		return nil, mapError(err)
	}
	return connect.NewResponse(&credentialv1.RenewTaskCredentialLeaseResponse{
		Valid: verdict.Valid, ExpiresAt: expiresProto(verdict.ExpiresAt),
	}), nil
}

func (h *LeaseHandler) ReleaseTaskCredentialLease(ctx context.Context, req *connect.Request[credentialv1.ReleaseTaskCredentialLeaseRequest]) (*connect.Response[credentialv1.ReleaseTaskCredentialLeaseResponse], error) {
	msg := req.Msg
	if msg.GetCredentialLeaseId() == "" || msg.GetTaskLeaseId() == "" || msg.GetWorkerId() == "" {
		return nil, connect.NewError(connect.CodeInvalidArgument, errInvalidLeaseRequest)
	}
	if err := h.issuer.Release(ctx, msg.GetCredentialLeaseId(), msg.GetTaskLeaseId(), msg.GetWorkerId()); err != nil {
		return nil, mapError(err)
	}
	return connect.NewResponse(&credentialv1.ReleaseTaskCredentialLeaseResponse{}), nil
}

// NewLeaseConnectHandler is the single construction path for the lease
// service; the composition root applies the pre-decode body budget.
func NewLeaseConnectHandler(issuer LeaseIssuer) (string, http.Handler) {
	return credentialv1connect.NewCredentialLeaseServiceHandler(NewLeaseHandler(issuer))
}

var errInvalidLeaseRequest = errorString("task credential lease request requires lease and worker identifiers")

type errorString string

func (e errorString) Error() string { return string(e) }

func expiresProto(value time.Time) *timestamppb.Timestamp {
	if value.IsZero() {
		return nil
	}
	return timestamppb.New(value)
}
