package main

import (
	"context"
	"github.com/google/uuid"
	previewapp "github.com/yangtao121/workos/internal/runtime/previewhost/application"
	previewports "github.com/yangtao121/workos/internal/runtime/previewhost/ports"
	workspaceapp "github.com/yangtao121/workos/internal/runtime/workspacehost/application"
	workspacedomain "github.com/yangtao121/workos/internal/runtime/workspacehost/domain"
	workspaceports "github.com/yangtao121/workos/internal/runtime/workspacehost/ports"
)

// Composition connects the private execution contract to module use cases;
// no preview module reads workspace tables or calls another module's adapter.
type workspaceOperationRouter struct {
	authorization workspaceports.Authorizer
	files         *workspaceapp.Execution
	host          *workspaceapp.Service
	previews      *previewapp.Service
}

func (r *workspaceOperationRouter) Execute(ctx context.Context, op workspacedomain.Operation) (workspacedomain.Result, error) {
	switch op.Name {
	case "preview.start", "preview.list", "preview.stop":
		if op.DelegationID != "" || op.ParentTaskID != "" {
			return nil, workspacedomain.ErrDenied
		}
	default:
		return r.files.Execute(ctx, op)
	}
	id, err := uuid.Parse(op.ID)
	if err != nil || id.Version() != 7 {
		return nil, workspacedomain.ErrInvalid
	}
	source, ok := r.host.Resolve(op.OwnerUserID, op.ProjectID)
	if !ok || source.ID != op.SourceID {
		return nil, workspacedomain.ErrDenied
	}
	if r.authorization == nil {
		return nil, workspacedomain.ErrUnavailable
	}
	grant, err := r.authorization.Resolve(ctx, op.OwnerUserID, op.ProjectID)
	if err != nil || grant.SourceID != op.SourceID || grant.BindingID != op.BindingID || grant.Revision != op.Revision {
		return nil, workspacedomain.ErrDenied
	}
	if r.previews == nil {
		return nil, workspacedomain.ErrUnavailable
	}
	str := func(key string) string { value, _ := op.Arguments[key].(string); return value }
	switch op.Name {
	case "preview.start":
		port, _ := op.Arguments["port"].(float64)
		if port != float64(int32(port)) {
			return nil, workspacedomain.ErrInvalid
		}
		record, err := r.previews.Start(ctx, op.OwnerUserID, op.ProjectID, str("outputKey"), str("command"), int32(port))
		if err != nil {
			return nil, err
		}
		return previewToolResult(record), nil
	case "preview.list":
		records, err := r.previews.List(ctx, op.OwnerUserID, op.ProjectID)
		if err != nil {
			return nil, err
		}
		items := make([]any, 0, len(records))
		for _, record := range records {
			items = append(items, map[string]any(previewToolResult(record)))
		}
		return workspacedomain.Result{"previews": items}, nil
	case "preview.stop":
		record, err := r.previews.Get(ctx, op.OwnerUserID, str("previewId"))
		if err != nil || record.ProjectID != op.ProjectID {
			return nil, workspacedomain.ErrDenied
		}
		if err := r.previews.Stop(ctx, op.OwnerUserID, record.PreviewID, str("outputKey")); err != nil {
			return nil, err
		}
		return workspacedomain.Result{"previewId": record.PreviewID, "state": "stopped"}, nil
	}
	return nil, workspacedomain.ErrInvalid
}
func previewToolResult(r previewports.PreviewRecord) workspacedomain.Result {
	// Model output contains only the public identity. The authenticated UI gets
	// a capability URL through the owner-facing preview API when opened.
	return workspacedomain.Result{"previewId": r.PreviewID, "projectId": r.ProjectID, "state": r.State, "generation": r.Generation, "port": r.Port}
}
