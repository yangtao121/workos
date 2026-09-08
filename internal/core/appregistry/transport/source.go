package transport

import (
	"context"
	"errors"
	"net/http"

	"connectrpc.com/connect"
	appv1 "github.com/yangtao121/workos/gen/go/workos/app/v1"
	"github.com/yangtao121/workos/gen/go/workos/app/v1/appv1connect"
	"github.com/yangtao121/workos/internal/core/appregistry/application"
	"github.com/yangtao121/workos/internal/core/appregistry/domain"
	"github.com/yangtao121/workos/internal/core/appregistry/ports"
	"github.com/yangtao121/workos/internal/platform/identity"
	"google.golang.org/protobuf/types/known/timestamppb"
)

type SourceHandler struct{ service *application.SourceService }

func NewSourceHandler(service *application.SourceService) (string, http.Handler) {
	return appv1connect.NewAppSourceBundleServiceHandler(&SourceHandler{service: service}, connect.WithReadMaxBytes(1024*1024))
}
func (h *SourceHandler) CreateAppSourceBundle(ctx context.Context, req *connect.Request[appv1.CreateAppSourceBundleRequest]) (*connect.Response[appv1.CreateAppSourceBundleResponse], error) {
	owner, err := identity.FromContext(ctx)
	if err != nil {
		return nil, connect.NewError(connect.CodeUnauthenticated, err)
	}
	files := make([]domain.SourceFile, 0, len(req.Msg.GetFiles()))
	for _, file := range req.Msg.GetFiles() {
		files = append(files, domain.SourceFile{Path: file.GetPath(), Content: file.GetContent(), Executable: file.GetExecutable()})
	}
	bundle, err := h.service.Create(ctx, owner.UserID, req.Msg.GetIdempotencyKey(), files)
	if err != nil {
		return nil, sourceError(err)
	}
	return connect.NewResponse(&appv1.CreateAppSourceBundleResponse{Bundle: sourceToProto(bundle)}), nil
}
func (h *SourceHandler) GetAppSourceBundle(ctx context.Context, req *connect.Request[appv1.GetAppSourceBundleRequest]) (*connect.Response[appv1.GetAppSourceBundleResponse], error) {
	owner, err := identity.FromContext(ctx)
	if err != nil {
		return nil, connect.NewError(connect.CodeUnauthenticated, err)
	}
	bundle, err := h.service.Get(ctx, owner.UserID, req.Msg.GetBundleId())
	if err != nil {
		return nil, sourceError(err)
	}
	return connect.NewResponse(&appv1.GetAppSourceBundleResponse{Bundle: sourceToProto(bundle)}), nil
}
func sourceToProto(bundle domain.SourceBundle) *appv1.AppSourceBundle {
	result := &appv1.AppSourceBundle{Id: bundle.ID, Digest: bundle.Digest, TotalSizeBytes: bundle.TotalSizeBytes, CreatedAt: timestamppb.New(bundle.CreatedAt)}
	for _, file := range bundle.Files {
		result.Files = append(result.Files, &appv1.AppSourceFile{Path: file.Path, Content: file.Content, Executable: file.Executable})
	}
	return result
}
func sourceError(err error) error {
	code := connect.CodeInternal
	switch {
	case errors.Is(err, domain.ErrInvalid):
		code = connect.CodeInvalidArgument
	case errors.Is(err, domain.ErrNotFound):
		code = connect.CodeNotFound
	case errors.Is(err, domain.ErrIdempotencyConflict):
		code = connect.CodeAborted
	case errors.Is(err, ports.ErrStoreUnavailable):
		code = connect.CodeUnavailable
	}
	return connect.NewError(code, errors.New("app source bundle request failed"))
}
