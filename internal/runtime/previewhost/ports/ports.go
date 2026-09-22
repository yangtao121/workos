// Package ports defines preview persistence, authorization and process seams.
package ports

import (
	"context"
	"github.com/yangtao121/workos/internal/runtime/previewhost/domain"
	"time"
)

type WorkspaceGrant struct {
	Directory, SourceID, BindingID string
	Revision                       int64
	ReadOnly                       bool
	Validate                       func(context.Context) error
}
type WorkspaceAuthorizer interface {
	AuthorizeWorkspace(context.Context, string, string) (WorkspaceGrant, error)
}
type PreviewRecord struct {
	LifecycleMode                                                        domain.LifecycleMode
	PreviewID, OwnerUserID, ProjectID, IdempotencyKey, WorkspaceSourceID string
	Command, AccessToken, RequestDigest, BindingID                       string
	Port                                                                 int32
	Generation, BindingRevision                                          int64
	ReadOnly                                                             bool
	State                                                                string
	CreatedAt, UpdatedAt, ExpiresAt                                      time.Time
}

func (r PreviewRecord) Live(now time.Time) bool {
	return r.State == domain.StateRunning && !r.LifecycleMode.Expired(r.ExpiresAt, now)
}
func (r PreviewRecord) EffectiveState(now time.Time) string {
	if (r.State == domain.StateRunning || r.State == domain.StateQueued) && r.LifecycleMode.Expired(r.ExpiresAt, now) {
		return domain.StateExpired
	}
	return r.State
}

type Store interface {
	FindByOwnerKey(context.Context, string, string) (PreviewRecord, bool, error)
	Insert(context.Context, PreviewRecord) (bool, error)
	Get(context.Context, string) (PreviewRecord, error)
	ListByProject(context.Context, string, string, int) ([]PreviewRecord, error)
	UpdateState(context.Context, string, string, string, time.Time) error
	Activate(context.Context, PreviewRecord) error
	ListActive(context.Context) ([]PreviewRecord, error)
	// Action atomically consumes the key, changes state, and advances generation
	// for restart. A duplicate never launches a process or reclaims a newer one.
	Action(context.Context, string, string, string, string, time.Time, ...domain.LifecycleMode) (PreviewRecord, bool, error)
}
type Request struct {
	Method, Path, Query string
	Headers             map[string]string
	Body                []byte
}
type Response struct {
	Status  int
	Headers map[string]string
	Body    []byte
}
type Process interface {
	Request(context.Context, Request) (Response, error)
	Stop()
	Exited() bool
}
type Engine interface {
	Launch(context.Context, PreviewRecord, WorkspaceGrant) (Process, error)
}
