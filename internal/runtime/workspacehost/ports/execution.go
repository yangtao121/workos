package ports

import (
	"context"
	"github.com/yangtao121/workos/internal/runtime/workspacehost/domain"
)

type Journal interface {
	// Begin is durable before any side effect. A pending prior attempt is
	// unknown, not permission to replay after a crash or a lost response.
	Begin(context.Context, domain.Operation) (domain.Result, bool, error)
	Complete(context.Context, domain.Operation, domain.Result) error
}

// Executor receives a Runtime-resolved root, never a caller-selected host path.
type Executor interface {
	Execute(context.Context, string, bool, domain.Operation) (domain.Result, error)
}

type Authorization struct {
	BindingID, SourceID string
	Revision            int64
	ReadOnly            bool
}
type Authorizer interface {
	Resolve(context.Context, string, string) (Authorization, error)
}

type DelegationStore interface {
	BeginDelegation(context.Context, domain.Delegation) (bool, error)
	GetDelegation(context.Context, string) (domain.Delegation, error)
	CompleteDelegation(context.Context, string, string) error
	ReviewDelegation(context.Context, string) error
}

type Worktree struct{ Root, GitDirectory, BaseCommit string }
type Worktrees interface {
	Prepare(context.Context, string, domain.Operation) (Worktree, error)
	Open(context.Context, domain.Delegation) (Worktree, error)
	Diff(context.Context, Worktree, domain.Operation) (domain.Result, error)
}
