package domain

import (
	"errors"
)

var ErrUnavailable = errors.New("workspace execution unavailable")
var ErrDenied = errors.New("workspace operation denied")
var ErrInvalid = errors.New("invalid workspace operation")
var ErrUnknownOutcome = errors.New("operation outcome unknown; inspect workspace before retrying")

type Operation struct {
	DelegationID, ParentTaskID string
	// Set only by the Runtime application after resolving a durable delegation.
	GitDirectory                               string `json:"-"`
	BindingID                                  string
	Revision                                   int64
	ID, OwnerUserID, ProjectID, SourceID, Name string
	ReadOnly                                   bool
	Arguments                                  map[string]any
}

type Result map[string]any

type Delegation struct {
	ID, TaskID, OwnerUserID, ProjectID, BindingID, SourceID string
	Revision                                                int64
	State, BaseCommit                                       string
}

func (d Delegation) Matches(op Operation) bool {
	return d.ID == op.DelegationID && d.TaskID == op.ParentTaskID && d.OwnerUserID == op.OwnerUserID && d.ProjectID == op.ProjectID && d.BindingID == op.BindingID && d.SourceID == op.SourceID && d.Revision == op.Revision
}
