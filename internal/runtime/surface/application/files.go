package application

import (
	"context"
	"github.com/yangtao121/workos/internal/runtime/surface/domain"
	"github.com/yangtao121/workos/internal/runtime/surface/ports"
	"strings"
)

func (s *BridgeService) fileScope(ctx context.Context, owner, device, token, capability string) (ports.FileScope, error) {
	session, err := s.authorizeCurrent(ctx, owner, device, token, capability)
	if err != nil {
		return ports.FileScope{}, err
	}
	scope := ports.FileScope{OwnerUserID: session.OwnerUserID, ProjectID: session.ProjectID}
	if s.workspace == nil {
		return ports.FileScope{}, domain.ErrUnavailable
	}
	available, writable := s.workspace.Access(scope)
	if !available {
		return ports.FileScope{}, domain.ErrUnavailable
	}
	if capability == "files.write" && !writable {
		return ports.FileScope{}, domain.ErrPermissionDenied
	}
	return scope, nil
}
func (s *BridgeService) ListFiles(ctx context.Context, owner, device, token, directory, after string) (domain.FilePage, error) {
	if !domain.ValidFilePath(directory, true) || !domain.ValidFilePath(after, true) || strings.Contains(after, "/") {
		return domain.FilePage{}, domain.ErrInvalid
	}
	scope, err := s.fileScope(ctx, owner, device, token, "files.pick")
	if err != nil {
		return domain.FilePage{}, err
	}
	return s.workspace.List(ctx, scope, directory, after)
}
func (s *BridgeService) ReadFile(ctx context.Context, owner, device, token string, ref domain.FileRef) ([]byte, error) {
	if !domain.ValidFilePath(ref.Path, false) || !domain.ValidFileETag(ref.ETag, false) {
		return nil, domain.ErrInvalid
	}
	scope, err := s.fileScope(ctx, owner, device, token, "files.read")
	if err != nil {
		return nil, err
	}
	if ref.ProjectID != scope.ProjectID {
		return nil, domain.ErrPermissionDenied
	}
	return s.workspace.Read(ctx, scope, ref)
}
func (s *BridgeService) WriteFile(ctx context.Context, owner, device, token string, ref domain.FileRef, data []byte) (domain.FileRef, error) {
	if !domain.ValidFilePath(ref.Path, false) || !domain.ValidFileETag(ref.ETag, true) {
		return domain.FileRef{}, domain.ErrInvalid
	}
	if len(data) > domain.MaxFileBytes {
		return domain.FileRef{}, domain.ErrFileLimit
	}
	scope, err := s.fileScope(ctx, owner, device, token, "files.write")
	if err != nil {
		return domain.FileRef{}, err
	}
	if ref.ProjectID != scope.ProjectID {
		return domain.FileRef{}, domain.ErrPermissionDenied
	}
	return s.workspace.Write(ctx, scope, ref, data)
}
