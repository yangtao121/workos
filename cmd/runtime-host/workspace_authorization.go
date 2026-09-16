package main

import (
	"context"
	nativeports "github.com/yangtao121/workos/internal/runtime/nativehost/ports"
	previewports "github.com/yangtao121/workos/internal/runtime/previewhost/ports"
	ptyports "github.com/yangtao121/workos/internal/runtime/ptyhost/ports"
	surfacedomain "github.com/yangtao121/workos/internal/runtime/surface/domain"
	surfaceports "github.com/yangtao121/workos/internal/runtime/surface/ports"
	workspaceapp "github.com/yangtao121/workos/internal/runtime/workspacehost/application"
	workspaceports "github.com/yangtao121/workos/internal/runtime/workspacehost/ports"
	"time"
)

type ptyWorkspaceAuthorization struct {
	host *workspaceapp.Service
	core workspaceports.Authorizer
}

func (a ptyWorkspaceAuthorization) AuthorizeWorkspace(ctx context.Context, owner, project string) (ptyports.WorkspaceGrant, error) {
	directory, readOnly, validate, err := a.host.AuthorizedDirectory(ctx, a.core, owner, project)
	return ptyports.WorkspaceGrant{Directory: directory, ReadOnly: readOnly, Validate: validate}, err
}

type nativeWorkspaceAuthorization struct {
	host *workspaceapp.Service
	core workspaceports.Authorizer
}

func (a nativeWorkspaceAuthorization) AuthorizeWorkspace(ctx context.Context, owner, project string) (nativeports.WorkspaceGrant, error) {
	directory, readOnly, validate, err := a.host.AuthorizedDirectory(ctx, a.core, owner, project)
	return nativeports.WorkspaceGrant{Directory: directory, ReadOnly: readOnly, Validate: validate}, err
}

type previewWorkspaceAuthorization struct {
	host *workspaceapp.Service
	core workspaceports.Authorizer
}

func (a previewWorkspaceAuthorization) AuthorizeWorkspace(ctx context.Context, owner, project string) (previewports.WorkspaceGrant, error) {
	if a.host == nil {
		return previewports.WorkspaceGrant{}, workspaceapp.ErrNoWorkspace
	}
	directory, readOnly, validate, err := a.host.AuthorizedDirectory(ctx, a.core, owner, project)
	if err != nil {
		return previewports.WorkspaceGrant{}, err
	}
	grant, err := a.core.Resolve(ctx, owner, project)
	if err != nil {
		return previewports.WorkspaceGrant{}, err
	}
	// Revalidate the pinned closure after reading the descriptive snapshot.
	if err := validate(ctx); err != nil {
		return previewports.WorkspaceGrant{}, err
	}
	return previewports.WorkspaceGrant{Directory: directory, ReadOnly: readOnly, Validate: validate, SourceID: grant.SourceID, BindingID: grant.BindingID, Revision: grant.Revision}, nil
}

// App files retain the narrower Bridge path/size policy while every access
// additionally requires Core's current project binding (including read-only).
type authorizedAppWorkspace struct {
	files         surfaceports.Workspace
	authorization previewWorkspaceAuthorization
}

func (a authorizedAppWorkspace) authorize(ctx context.Context, scope surfaceports.FileScope, write bool) error {
	grant, err := a.authorization.AuthorizeWorkspace(ctx, scope.OwnerUserID, scope.ProjectID)
	if err != nil {
		return surfacedomain.ErrPermissionDenied
	}
	if write && grant.ReadOnly {
		return surfacedomain.ErrPermissionDenied
	}
	return nil
}
func (a authorizedAppWorkspace) Access(scope surfaceports.FileScope) (bool, bool) {
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	if a.authorize(ctx, scope, false) != nil {
		return false, false
	}
	available, writable := a.files.Access(scope)
	return available, writable && a.authorize(ctx, scope, true) == nil
}
func (a authorizedAppWorkspace) List(ctx context.Context, scope surfaceports.FileScope, directory, after string) (surfacedomain.FilePage, error) {
	if err := a.authorize(ctx, scope, false); err != nil {
		return surfacedomain.FilePage{}, err
	}
	return a.files.List(ctx, scope, directory, after)
}
func (a authorizedAppWorkspace) Read(ctx context.Context, scope surfaceports.FileScope, ref surfacedomain.FileRef) ([]byte, error) {
	if err := a.authorize(ctx, scope, false); err != nil {
		return nil, err
	}
	return a.files.Read(ctx, scope, ref)
}
func (a authorizedAppWorkspace) Write(ctx context.Context, scope surfaceports.FileScope, ref surfacedomain.FileRef, data []byte) (surfacedomain.FileRef, error) {
	if err := a.authorize(ctx, scope, true); err != nil {
		return surfacedomain.FileRef{}, err
	}
	return a.files.Write(ctx, scope, ref, data)
}
