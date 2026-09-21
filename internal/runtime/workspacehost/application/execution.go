package application

import (
	"context"
	"strings"

	"github.com/google/uuid"
	"github.com/yangtao121/workos/internal/runtime/workspacehost/domain"
	"github.com/yangtao121/workos/internal/runtime/workspacehost/ports"
)

type Execution struct {
	delegations     ports.DelegationStore
	worktrees       ports.Worktrees
	authorization   ports.Authorizer
	sources         *Service
	files, commands ports.Executor
	journal         ports.Journal
	slots           chan struct{}
}

func NewExecution(sources *Service, files, commands ports.Executor, journal ports.Journal) *Execution {
	return &Execution{sources: sources, files: files, commands: commands, journal: journal, slots: make(chan struct{}, 4)}
}
func (e *Execution) Execute(ctx context.Context, op domain.Operation) (domain.Result, error) {
	id, err := uuid.Parse(op.ID)
	if err != nil || id.Version() != 7 {
		return nil, domain.ErrInvalid
	}
	source, ok := e.sources.Resolve(op.OwnerUserID, op.ProjectID)
	if !ok || source.ID != op.SourceID {
		return nil, domain.ErrDenied
	}
	if e.authorization != nil {
		grant, err := e.authorization.Resolve(ctx, op.OwnerUserID, op.ProjectID)
		if err != nil || grant.BindingID != op.BindingID || grant.Revision != op.Revision || grant.SourceID != op.SourceID {
			return nil, domain.ErrDenied
		}
		op.ReadOnly = op.ReadOnly || grant.ReadOnly
	}
	var executor ports.Executor
	switch op.Name {
	case "delegation.create", "delegation.inspect", "delegation.diff", "shell.run":
		executor = e.commands
	case "fs.resolve", "fs.stat", "fs.list", "fs.read", "fs.write", "fs.edit":
		executor = e.files
	default:
		return nil, domain.ErrInvalid
	}
	if executor == nil || e.journal == nil {
		return nil, domain.ErrUnavailable
	}
	select {
	case e.slots <- struct{}{}:
		defer func() { <-e.slots }()
	default:
		return nil, domain.ErrUnavailable
	}
	readOnly := source.ReadOnly || op.ReadOnly
	root := source.Path
	if op.DelegationID != "" || op.ParentTaskID != "" || strings.HasPrefix(op.Name, "delegation.") {
		tree, result, err := e.delegation(ctx, root, readOnly, op)
		if err != nil || result != nil {
			return result, err
		}
		root, op.GitDirectory = tree.Root, tree.GitDirectory
	}

	mutating := op.Name == "shell.run" || op.Name == "fs.write" || op.Name == "fs.edit"
	if readOnly && strings.HasPrefix(op.Name, "fs.") && mutating {
		return nil, domain.ErrDenied
	}
	if !mutating {
		return executor.Execute(ctx, root, readOnly, op)
	}
	cached, fresh, err := e.journal.Begin(ctx, op)
	if err != nil || !fresh {
		return cached, err
	}
	result, err := executor.Execute(ctx, root, readOnly, op)
	// An infrastructure error after admission is deliberately left pending:
	// restart/retry cannot prove that a partial side effect did not happen.
	if err != nil {
		return nil, err
	}
	if err := e.journal.Complete(ctx, op, result); err != nil {
		return nil, err
	}
	return result, nil
}

func (e *Execution) WithAuthorization(a ports.Authorizer) *Execution { e.authorization = a; return e }
