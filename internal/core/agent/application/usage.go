package application

import (
	"context"
	"errors"
	"time"

	"github.com/yangtao121/workos/internal/core/agent/domain"
	"github.com/yangtao121/workos/internal/core/agent/ports"
)

var errUsageDependencies = errors.New("usage service requires repository and installation dependencies")

// DailyUsageReport is the owner-visible projection of one quota bucket.
// Reserved allowance and observed usage are separate facts; cost is only
// available when a verified observation exists.
type DailyUsageReport struct {
	UTCDate              string
	TasksReserved        int64
	OutputTokensReserved int64
	TasksRecorded        int64
	InputTokensRecorded  int64
	OutputTokensRecorded int64
	CostDecimalRecorded  string
	CostAvailable        bool
	QuotaBreached        bool
}

// UsageService reads the per-bucket usage projection together with the
// effective policy identity the reservation ceiling comes from.
type UsageService struct {
	repository    ports.Repository
	installations ports.InstallationSource
}

func NewUsageService(repository ports.Repository, installations ports.InstallationSource) (*UsageService, error) {
	if repository == nil || installations == nil {
		return nil, errUsageDependencies
	}
	return &UsageService{repository: repository, installations: installations}, nil
}

// Policies exposes the policy service so the transport can reuse one handler
// wiring for both reads.
func (s *UsageService) Policies() *PolicyService {
	return &PolicyService{repository: s.repository, installations: s.installations}
}

// AppDailyUsageWithPolicy resolves installation liveness, the effective
// policy (for the reservation ceiling's revision), and the bucket projection.
func (s *UsageService) AppDailyUsageWithPolicy(ctx context.Context, ownerUserID, projectID, appInstanceID, utcDate string) (domain.Policy, DailyUsageReport, error) {
	policy, err := s.effectivePolicyForUsage(ctx, ownerUserID, projectID, appInstanceID)
	if err != nil {
		return domain.Policy{}, DailyUsageReport{}, err
	}
	if utcDate == "" {
		utcDate = time.Now().UTC().Format("2006-01-02")
	}
	if _, err := time.Parse("2006-01-02", utcDate); err != nil {
		return domain.Policy{}, DailyUsageReport{}, domain.ErrInvalid
	}
	usage, err := s.repository.GetAppDailyUsage(ctx, ownerUserID, appInstanceID, utcDate)
	if err != nil {
		return domain.Policy{}, DailyUsageReport{}, err
	}
	return policy, DailyUsageReport{
		UTCDate:              usage.UTCDate,
		TasksReserved:        usage.TasksReserved,
		OutputTokensReserved: usage.OutputTokensReserved,
		TasksRecorded:        usage.TasksRecorded,
		InputTokensRecorded:  usage.InputTokensRecorded,
		OutputTokensRecorded: usage.OutputTokensRecorded,
		CostDecimalRecorded:  usage.CostDecimalRecorded,
		CostAvailable:        usage.CostAvailable,
		QuotaBreached:        usage.QuotaBreached,
	}, nil
}

func (s *UsageService) effectivePolicyForUsage(ctx context.Context, ownerUserID, projectID, appInstanceID string) (domain.Policy, error) {
	policy, _, err := effectivePolicy(ctx, s.repository, s.installations, ownerUserID, projectID, appInstanceID)
	return policy, err
}
