package ports

import (
	"context"
	"github.com/yangtao121/workos/internal/runtime/surface/domain"
)

type FileScope struct{ OwnerUserID, ProjectID string }

// Workspace owns only explicitly configured local project roots. Host paths
// never appear in the application or public file references.
type Workspace interface {
	Access(scope FileScope) (available, writable bool)
	List(ctx context.Context, scope FileScope, directory, after string) (domain.FilePage, error)
	Read(ctx context.Context, scope FileScope, ref domain.FileRef) ([]byte, error)
	Write(ctx context.Context, scope FileScope, ref domain.FileRef, data []byte) (domain.FileRef, error)
}
