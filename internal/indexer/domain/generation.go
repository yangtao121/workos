package domain

import "time"

// GenerationStatus is the bounded operational projection for the local
// admin socket. Scope identifiers are intentionally absent.
type GenerationStatus struct {
	ID             string
	Scope          string
	Status         string
	DocumentCount  int64
	TombstoneCount int64
	CreatedAt      time.Time
	PromotedAt     time.Time
}

func ValidGenerationStatus(generation GenerationStatus) error {
	if !ValidUUID(generation.ID) || generation.DocumentCount < 0 || generation.TombstoneCount < 0 || generation.CreatedAt.IsZero() {
		return ErrCorrupt
	}
	if generation.Scope != "all" && generation.Scope != "project" {
		return ErrCorrupt
	}
	switch generation.Status {
	case "building", "active", "retired", "failed", "canceled":
	default:
		return ErrCorrupt
	}
	return nil
}
