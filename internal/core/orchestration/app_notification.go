// The neutral adapter between the Core notification ingest authority and
// the installation/grant facts (ADR-0014). The notification module never
// imports Project or App Registry packages; it sees only this port.
package orchestration

import (
	"context"
	"errors"

	notificationports "github.com/yangtao121/workos/internal/core/notification/ports"
	projectdomain "github.com/yangtao121/workos/internal/core/project/domain"
	"github.com/yangtao121/workos/internal/platform/dbtx"
)

// AppNotificationAuthorizer implements the notification module's
// AppInstallationAuthorizer port by delegating to the App Agent service's
// authoritative authorization chain.
type AppNotificationAuthorizer struct {
	agent *AppAgentService
}

func NewAppNotificationAuthorizer(agent *AppAgentService) (*AppNotificationAuthorizer, error) {
	if agent == nil {
		return nil, errors.New("app notification authorizer requires the app agent service")
	}
	return &AppNotificationAuthorizer{agent: agent}, nil
}

func (a *AppNotificationAuthorizer) AuthorizeAppNotificationTx(ctx context.Context, tx dbtx.Tx, ownerUserID, projectID, appInstanceID string, installationGrantRevision int64) (notificationports.AppInstallationFacts, error) {
	_, _, appID, err := a.agent.AuthorizeAppNotificationForIngestTx(ctx, tx, ownerUserID, projectID, appInstanceID, installationGrantRevision)
	if err != nil {
		// Transient upstream failures stay retryable; every denial verdict
		// collapses onto the one sanitized sentinel so the caller can never
		// distinguish missing installation, stale epoch, or missing grant.
		if errors.Is(err, notificationports.ErrStoreUnavailable) {
			return notificationports.AppInstallationFacts{}, err
		}
		if errors.Is(err, projectdomain.ErrNotFound) || errors.Is(err, projectdomain.ErrInvalid) ||
			errors.Is(err, ErrAppGrantStale) || errors.Is(err, ErrAppNotGranted) ||
			errors.Is(err, errAppGrantCorrupt) {
			return notificationports.AppInstallationFacts{}, notificationports.ErrAppNotificationDenied
		}
		return notificationports.AppInstallationFacts{}, err
	}
	return notificationports.AppInstallationFacts{AppID: appID}, nil
}
