package ports

import (
	"context"
	artifactv1 "github.com/yangtao121/workos/gen/go/workos/artifact/v1"
)

type AppArtifactQuery struct {
	ProjectID, AppInstanceID string
	GrantRevision            int64
}
type AppArtifactInput struct {
	Key, Type, Title string
	Content          []byte
}
type AppArtifactClient interface {
	Create(context.Context, AppArtifactQuery, AppArtifactInput) (*artifactv1.Artifact, error)
	Open(context.Context, AppArtifactQuery, string) (*artifactv1.Artifact, error)
}
