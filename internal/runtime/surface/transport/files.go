package transport

import (
	"connectrpc.com/connect"
	"context"
	bridgev1 "github.com/yangtao121/workos/gen/go/workos/bridge/v1"
	"github.com/yangtao121/workos/internal/platform/identity"
	"github.com/yangtao121/workos/internal/runtime/surface/domain"
)

func (h *BridgeHandler) ListFiles(ctx context.Context, req *connect.Request[bridgev1.ListFilesRequest]) (*connect.Response[bridgev1.ListFilesResponse], error) {
	id, err := identity.FromContext(ctx)
	if err != nil {
		return nil, mapBridgeError(domain.ErrUnauthenticated)
	}
	page, err := h.service.ListFiles(ctx, id.UserID, id.DeviceID, req.Header().Get(identity.BridgeTokenHeader), req.Msg.GetDirectory(), req.Msg.GetAfter())
	if err != nil {
		return nil, mapBridgeError(err)
	}
	response := &bridgev1.ListFilesResponse{NextAfter: page.NextAfter}
	for _, entry := range page.Entries {
		response.Entries = append(response.Entries, &bridgev1.FileEntry{Ref: fileRefToProto(entry.Ref), Directory: entry.Directory, SizeBytes: entry.Size})
	}
	return connect.NewResponse(response), nil
}
func (h *BridgeHandler) ReadFile(ctx context.Context, req *connect.Request[bridgev1.ReadFileRequest]) (*connect.Response[bridgev1.ReadFileResponse], error) {
	id, err := identity.FromContext(ctx)
	if err != nil {
		return nil, mapBridgeError(domain.ErrUnauthenticated)
	}
	data, err := h.service.ReadFile(ctx, id.UserID, id.DeviceID, req.Header().Get(identity.BridgeTokenHeader), fileRefFromProto(req.Msg.GetRef()))
	if err != nil {
		return nil, mapBridgeError(err)
	}
	return connect.NewResponse(&bridgev1.ReadFileResponse{Data: data}), nil
}
func (h *BridgeHandler) WriteFile(ctx context.Context, req *connect.Request[bridgev1.WriteFileRequest]) (*connect.Response[bridgev1.WriteFileResponse], error) {
	id, err := identity.FromContext(ctx)
	if err != nil {
		return nil, mapBridgeError(domain.ErrUnauthenticated)
	}
	ref, err := h.service.WriteFile(ctx, id.UserID, id.DeviceID, req.Header().Get(identity.BridgeTokenHeader), fileRefFromProto(req.Msg.GetRef()), req.Msg.GetData())
	if err != nil {
		return nil, mapBridgeError(err)
	}
	return connect.NewResponse(&bridgev1.WriteFileResponse{Ref: fileRefToProto(ref)}), nil
}
func fileRefFromProto(ref *bridgev1.FileRef) domain.FileRef {
	return domain.FileRef{ProjectID: ref.GetProjectId(), Path: ref.GetPath(), ETag: ref.GetEtag()}
}
func fileRefToProto(ref domain.FileRef) *bridgev1.FileRef {
	return &bridgev1.FileRef{ProjectId: ref.ProjectID, Path: ref.Path, Etag: ref.ETag}
}
