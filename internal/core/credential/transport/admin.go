// Package transport exposes the Credential Vault's two private faces: the
// operator admin service on the Core-owned Unix socket, and the harness
// credential lease service on the Core private mTLS execution listener.
// Neither is ever registered on the ordinary Core HTTP listener and neither
// ever enters the Gateway allowlist.
package transport

import (
	"context"
	"errors"
	"net/http"
	"time"

	"connectrpc.com/connect"
	"google.golang.org/protobuf/types/known/timestamppb"

	credentialv1 "github.com/yangtao121/workos/gen/go/workos/credential/v1"
	"github.com/yangtao121/workos/gen/go/workos/credential/v1/credentialv1connect"
	"github.com/yangtao121/workos/internal/core/credential/domain"
	"github.com/yangtao121/workos/internal/core/credential/ports"
)

// AdminService is the vault's admin application contract. The composition
// root fixes the single owner; no admin RPC accepts an owner.
type AdminService interface {
	Put(ctx context.Context, command ports.PutCommand) (domain.Credential, error)
	Rotate(ctx context.Context, command ports.RotateCommand) (domain.Credential, error)
	Revoke(ctx context.Context, command ports.RevokeCommand) (domain.Credential, error)
	List(ctx context.Context, ownerUserID string) ([]domain.Credential, error)
}

// AdminHandler serves CredentialAdminService. Secrets arrive only inside
// bounded request bytes on the admin socket; responses are metadata only.
type AdminHandler struct {
	service AdminService
	ownerID string
}

// MaxAdminRequestBytes bounds every admin request before decoding. The
// largest legal secret is 8 KiB; 16 KiB covers base64 wire inflation of the
// protobuf bytes field plus identity fields with headroom.
const MaxAdminRequestBytes = 16 * 1024

func NewAdminHandler(service AdminService, ownerID string) *AdminHandler {
	return &AdminHandler{service: service, ownerID: ownerID}
}

func (h *AdminHandler) PutCredential(ctx context.Context, req *connect.Request[credentialv1.PutCredentialRequest]) (*connect.Response[credentialv1.PutCredentialResponse], error) {
	msg := req.Msg
	credential, err := h.service.Put(ctx, ports.PutCommand{
		OwnerUserID:    h.ownerID,
		ConsumerID:     msg.GetConsumerId(),
		Purpose:        msg.GetPurpose(),
		Label:          msg.GetLabel(),
		Secret:         msg.GetSecret(),
		IdempotencyKey: msg.GetIdempotencyKey(),
	})
	if err != nil {
		return nil, mapError(err)
	}
	return connect.NewResponse(&credentialv1.PutCredentialResponse{Credential: metadataProto(credential)}), nil
}

func (h *AdminHandler) RotateCredential(ctx context.Context, req *connect.Request[credentialv1.RotateCredentialRequest]) (*connect.Response[credentialv1.RotateCredentialResponse], error) {
	msg := req.Msg
	credential, err := h.service.Rotate(ctx, ports.RotateCommand{
		OwnerUserID:      h.ownerID,
		CredentialID:     msg.GetCredentialId(),
		Label:            msg.GetLabel(),
		Secret:           msg.GetSecret(),
		ExpectedRevision: msg.GetExpectedRevision(),
		IdempotencyKey:   msg.GetIdempotencyKey(),
	})
	if err != nil {
		return nil, mapError(err)
	}
	return connect.NewResponse(&credentialv1.RotateCredentialResponse{Credential: metadataProto(credential)}), nil
}

func (h *AdminHandler) RevokeCredential(ctx context.Context, req *connect.Request[credentialv1.RevokeCredentialRequest]) (*connect.Response[credentialv1.RevokeCredentialResponse], error) {
	msg := req.Msg
	credential, err := h.service.Revoke(ctx, ports.RevokeCommand{
		OwnerUserID:      h.ownerID,
		CredentialID:     msg.GetCredentialId(),
		ExpectedRevision: msg.GetExpectedRevision(),
		IdempotencyKey:   msg.GetIdempotencyKey(),
	})
	if err != nil {
		return nil, mapError(err)
	}
	return connect.NewResponse(&credentialv1.RevokeCredentialResponse{Credential: metadataProto(credential)}), nil
}

func (h *AdminHandler) ListCredentials(ctx context.Context, req *connect.Request[credentialv1.ListCredentialsRequest]) (*connect.Response[credentialv1.ListCredentialsResponse], error) {
	credentials, err := h.service.List(ctx, h.ownerID)
	if err != nil {
		return nil, mapError(err)
	}
	response := &credentialv1.ListCredentialsResponse{}
	for _, credential := range credentials {
		response.Credentials = append(response.Credentials, metadataProto(credential))
	}
	return connect.NewResponse(response), nil
}

// NewAdminConnectHandler is the single construction path for the admin
// service; the composition root applies the pre-decode body budget.
func NewAdminConnectHandler(service AdminService, ownerID string) (string, http.Handler) {
	return credentialv1connect.NewCredentialAdminServiceHandler(NewAdminHandler(service, ownerID))
}

func metadataProto(credential domain.Credential) *credentialv1.CredentialMetadata {
	status := credentialv1.CredentialStatus_CREDENTIAL_STATUS_UNSPECIFIED
	switch credential.Status {
	case domain.StatusActive:
		status = credentialv1.CredentialStatus_CREDENTIAL_STATUS_ACTIVE
	case domain.StatusRevoked:
		status = credentialv1.CredentialStatus_CREDENTIAL_STATUS_REVOKED
	}
	return &credentialv1.CredentialMetadata{
		Id: credential.ID, OwnerUserId: credential.OwnerUserID, ConsumerId: credential.ConsumerID,
		Purpose: credential.Purpose, Label: credential.Label, Revision: credential.Revision,
		Status:    status,
		CreatedAt: timestampProto(credential.CreatedAt), UpdatedAt: timestampProto(credential.UpdatedAt),
	}
}

func timestampProto(value time.Time) *timestamppb.Timestamp {
	if value.IsZero() {
		return nil
	}
	return timestamppb.New(value)
}

// mapError is the fixed sanitized matrix. Secrets never appear in any
// message; unknown failures are opaque Internal.
func mapError(err error) error {
	switch {
	case err == nil:
		return nil
	case errors.Is(err, domain.ErrInvalid):
		return connect.NewError(connect.CodeInvalidArgument, domain.ErrInvalid)
	case errors.Is(err, domain.ErrNotFound):
		return connect.NewError(connect.CodeNotFound, domain.ErrNotFound)
	case errors.Is(err, domain.ErrConflict), errors.Is(err, domain.ErrIdempotencyConflict):
		return connect.NewError(connect.CodeAborted, errors.New("credential was modified concurrently; retry with the current revision"))
	case errors.Is(err, domain.ErrAlreadyExists):
		return connect.NewError(connect.CodeFailedPrecondition, domain.ErrAlreadyExists)
	case errors.Is(err, domain.ErrLeaseLost):
		return connect.NewError(connect.CodeFailedPrecondition, domain.ErrLeaseLost)
	case errors.Is(err, ports.ErrStoreUnavailable), errors.Is(err, domain.ErrUnavailable):
		return connect.NewError(connect.CodeUnavailable, domain.ErrUnavailable)
	default:
		return connect.NewError(connect.CodeInternal, errors.New("credential operation failed"))
	}
}
