package orchestration

import (
	"context"
	"errors"
	"time"

	artifactdomain "github.com/yangtao121/workos/internal/core/artifact/domain"
	artifactports "github.com/yangtao121/workos/internal/core/artifact/ports"
	indexdomain "github.com/yangtao121/workos/internal/core/indexfeed/domain"
	notificationdomain "github.com/yangtao121/workos/internal/core/notification/domain"
	"github.com/yangtao121/workos/internal/platform/dbtx"
	"github.com/yangtao121/workos/internal/platform/ids"
)

type AppArtifactStore interface {
	FindAppOutput(context.Context, dbtx.Tx, string, string) (artifactports.AppOutputRecord, bool, error)
	InsertAppOutput(context.Context, dbtx.Tx, artifactports.AppOutputCommand) error
	ReviewArtifactByID(context.Context, dbtx.Tx, string) (artifactdomain.ReviewArtifact, error)
}
type AppArtifactAuthorizer interface {
	AuthorizeAppWriteTx(context.Context, dbtx.Tx, string, string, string, int64, string) (string, string, string, error)
}
type AppArtifactScope struct {
	OwnerUserID, ProjectID, AppInstanceID string
	GrantRevision                         int64
}
type AppArtifactService struct {
	pool          TaskTxSource
	authorizer    AppArtifactAuthorizer
	store         AppArtifactStore
	feed          IndexPublicationSink
	notifications NotificationSink
	ids           ids.Generator
}

func NewAppArtifactService(pool TaskTxSource, authorizer AppArtifactAuthorizer, store AppArtifactStore, feed IndexPublicationSink, notifications NotificationSink, generator ids.Generator) (*AppArtifactService, error) {
	if pool == nil || authorizer == nil || store == nil || feed == nil || notifications == nil || generator == nil {
		return nil, errors.New("app artifacts require authorization, storage and publication ports")
	}
	return &AppArtifactService{pool, authorizer, store, feed, notifications, generator}, nil
}
func (s *AppArtifactService) authorize(ctx context.Context, tx dbtx.Tx, scope AppArtifactScope, capability string) error {
	if !artifactdomain.ValidArtifactUUID(scope.OwnerUserID) || !artifactdomain.ValidArtifactUUID(scope.ProjectID) || !artifactdomain.ValidArtifactUUID(scope.AppInstanceID) || scope.GrantRevision <= 0 {
		return artifactdomain.ErrInvalid
	}
	_, _, _, err := s.authorizer.AuthorizeAppWriteTx(ctx, tx, scope.OwnerUserID, scope.ProjectID, scope.AppInstanceID, scope.GrantRevision, capability)
	return err
}
func (s *AppArtifactService) Create(ctx context.Context, scope AppArtifactScope, key, kind, rawTitle string, content []byte) (artifactdomain.ReviewArtifact, error) {
	title, ok := artifactdomain.NormalizeReviewTitle(rawTitle)
	if !ok || !artifactdomain.ValidReviewOutputKey(key) || len(content) > 32*1024 {
		return artifactdomain.ReviewArtifact{}, artifactdomain.ErrInvalid
	}
	normalized, err := artifactdomain.NormalizeReviewContent(kind, content)
	if err != nil {
		return artifactdomain.ReviewArtifact{}, err
	}
	requestDigest := artifactdomain.ReviewOutputRequestDigest(scope.ProjectID, scope.AppInstanceID, key, title, normalized.Digest)
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return artifactdomain.ReviewArtifact{}, err
	}
	defer tx.Rollback(ctx)
	if err := s.authorize(ctx, tx, scope, "artifact.write"); err != nil {
		return artifactdomain.ReviewArtifact{}, err
	}
	previous, found, err := s.store.FindAppOutput(ctx, tx, scope.AppInstanceID, key)
	if err != nil {
		return artifactdomain.ReviewArtifact{}, err
	}
	if found {
		if previous.OwnerUserID != scope.OwnerUserID || previous.ProjectID != scope.ProjectID {
			return artifactdomain.ReviewArtifact{}, artifactdomain.ErrCorrupt
		}
		if previous.RequestDigest != requestDigest {
			return artifactdomain.ReviewArtifact{}, artifactdomain.ErrIdempotencyConflict
		}
		a, err := s.store.ReviewArtifactByID(ctx, tx, previous.ArtifactID)
		if err != nil {
			return artifactdomain.ReviewArtifact{}, err
		}
		if !artifactdomain.ValidStoredReviewFact(a) || a.OwnerUserID != scope.OwnerUserID || a.ProjectID != scope.ProjectID || a.SourceAppInstanceID != scope.AppInstanceID || a.OutputKey != key || a.Title != title || a.Digest != normalized.Digest {
			return artifactdomain.ReviewArtifact{}, artifactdomain.ErrCorrupt
		}
		return a, nil
	}
	_, media, _ := artifactdomain.ReviewType(kind)
	now := artifactdomain.CanonicalUTCTime(time.Now().UTC())
	a := artifactdomain.ReviewArtifact{ID: s.ids.New(), OwnerUserID: scope.OwnerUserID, ProjectID: scope.ProjectID, SourceAppInstanceID: scope.AppInstanceID, OutputKey: key, Type: kind, Title: title, MediaType: media, Digest: normalized.Digest, ByteCount: normalized.ByteCount, LineCount: normalized.LineCount, CreatedAt: now}
	if err := s.store.InsertAppOutput(ctx, tx, artifactports.AppOutputCommand{Artifact: a, Content: normalized.Content, RequestDigest: requestDigest}); err != nil {
		return artifactdomain.ReviewArtifact{}, err
	}
	if err := s.feed.AppendReviewArtifactUpsert(ctx, tx, indexdomain.Publication{ID: s.ids.New(), Operation: indexdomain.OperationReviewArtifactUpsert, OwnerUserID: a.OwnerUserID, ProjectID: a.ProjectID, SourceType: indexdomain.SourceType, SourceID: a.ID, ArtifactType: a.Type, Digest: a.Digest, OccurredAt: now}); err != nil {
		return artifactdomain.ReviewArtifact{}, err
	}
	if _, err := s.notifications.AppendSystemNotification(ctx, tx, notificationdomain.SystemFact{Kind: notificationdomain.KindArtifactReviewCreated, OwnerUserID: a.OwnerUserID, ProjectID: a.ProjectID, Category: a.Type, TargetID: a.ID, SourceID: a.ID}, now); err != nil {
		return artifactdomain.ReviewArtifact{}, err
	}
	if err := tx.Commit(ctx); err != nil {
		return artifactdomain.ReviewArtifact{}, err
	}
	return a, nil
}
func (s *AppArtifactService) Open(ctx context.Context, scope AppArtifactScope, id string) (artifactdomain.ReviewArtifact, error) {
	if !artifactdomain.ValidArtifactUUID(id) {
		return artifactdomain.ReviewArtifact{}, artifactdomain.ErrInvalid
	}
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return artifactdomain.ReviewArtifact{}, err
	}
	defer tx.Rollback(ctx)
	if err := s.authorize(ctx, tx, scope, "artifact.read"); err != nil {
		return artifactdomain.ReviewArtifact{}, err
	}
	a, err := s.store.ReviewArtifactByID(ctx, tx, id)
	if err != nil {
		return artifactdomain.ReviewArtifact{}, err
	}
	if a.OwnerUserID != scope.OwnerUserID || a.ProjectID != scope.ProjectID {
		return artifactdomain.ReviewArtifact{}, artifactdomain.ErrNotFound
	}
	return a, nil
}
