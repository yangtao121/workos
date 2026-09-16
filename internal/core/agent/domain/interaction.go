package domain

import "time"

type QuestionChoice struct {
	Label       string `json:"label"`
	Description string `json:"description,omitempty"`
}
type ExecutionQuestion struct {
	ID       string           `json:"id"`
	Text     string           `json:"text"`
	Detail   string           `json:"detail,omitempty"`
	Choices  []QuestionChoice `json:"choices,omitempty"`
	Multiple bool             `json:"multiple,omitempty"`
}
type ExecutionAnswer struct {
	QuestionID string   `json:"questionId"`
	Selected   []string `json:"selected"`
	Text       string   `json:"text,omitempty"`
}
type ExecutionInteraction struct {
	ID, TaskID, OwnerUserID, ProjectID, LeaseID, WorkerID, RequestKey, State, DecisionKey, DecisionDigest string
	Questions                                                                                             []ExecutionQuestion
	Answers                                                                                               []ExecutionAnswer
	CreatedAt, ExpiresAt                                                                                  time.Time
}
