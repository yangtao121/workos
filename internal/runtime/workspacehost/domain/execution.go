package domain

import (
	"errors"
)

var ErrUnavailable = errors.New("workspace execution unavailable")
var ErrDenied = errors.New("workspace operation denied")
var ErrInvalid = errors.New("invalid workspace operation")
var ErrUnknownOutcome = errors.New("operation outcome unknown; inspect workspace before retrying")

type Operation struct {
	BindingID                                  string
	Revision                                   int64
	ID, OwnerUserID, ProjectID, SourceID, Name string
	ReadOnly                                   bool
	Arguments                                  map[string]any
}

type Result map[string]any
