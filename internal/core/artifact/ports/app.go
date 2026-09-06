package ports

import "github.com/yangtao121/workos/internal/core/artifact/domain"

type AppOutputCommand struct {
	Artifact      domain.ReviewArtifact
	Content       []byte
	RequestDigest string
}

type AppOutputRecord struct {
	ArtifactID    string
	OwnerUserID   string
	ProjectID     string
	RequestDigest string
}
