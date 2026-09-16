package transport

import (
	"connectrpc.com/connect"
	"context"
	"errors"
	agentv1 "github.com/yangtao121/workos/gen/go/workos/agent/v1"
	"github.com/yangtao121/workos/gen/go/workos/agent/v1/agentv1connect"
	"github.com/yangtao121/workos/internal/core/agent/application"
	"github.com/yangtao121/workos/internal/core/agent/domain"
	"github.com/yangtao121/workos/internal/platform/identity"
	"google.golang.org/protobuf/types/known/timestamppb"
	"net/http"
)

type interactionHandler struct {
	service *application.InteractionService
}

func NewInteractionHandler(service *application.InteractionService) (string, http.Handler) {
	return agentv1connect.NewAgentInteractionServiceHandler(&interactionHandler{service}, connect.WithReadMaxBytes(64*1024))
}
func interactionProto(r domain.ExecutionInteraction) *agentv1.ExecutionInteraction {
	result := &agentv1.ExecutionInteraction{Id: r.ID, TaskId: r.TaskID, ProjectId: r.ProjectID, State: r.State, ExpiresAt: timestamppb.New(r.ExpiresAt)}
	for _, q := range r.Questions {
		question := &agentv1.ExecutionQuestion{Id: q.ID, Text: q.Text, Detail: q.Detail, Multiple: q.Multiple}
		for _, choice := range q.Choices {
			question.Choices = append(question.Choices, &agentv1.ExecutionQuestionChoice{Label: choice.Label, Description: choice.Description})
		}
		result.Questions = append(result.Questions, question)
	}
	return result
}
func interactionError(err error) error {
	code := connect.CodeFailedPrecondition
	if errors.Is(err, domain.ErrNotFound) {
		code = connect.CodeNotFound
	}
	if errors.Is(err, domain.ErrInvalid) {
		code = connect.CodeInvalidArgument
	}
	return connect.NewError(code, errors.New("execution question unavailable or no longer pending"))
}
func (h *interactionHandler) ListTaskInteractions(ctx context.Context, req *connect.Request[agentv1.ListTaskInteractionsRequest]) (*connect.Response[agentv1.ListTaskInteractionsResponse], error) {
	owner, err := identity.FromContext(ctx)
	if err != nil {
		return nil, connect.NewError(connect.CodeUnauthenticated, err)
	}
	records, err := h.service.List(ctx, owner.UserID, req.Msg.GetTaskId())
	if err != nil {
		return nil, interactionError(err)
	}
	result := &agentv1.ListTaskInteractionsResponse{}
	for _, r := range records {
		result.Interactions = append(result.Interactions, interactionProto(r))
	}
	return connect.NewResponse(result), nil
}
func (h *interactionHandler) RespondExecutionInteraction(ctx context.Context, req *connect.Request[agentv1.RespondExecutionInteractionRequest]) (*connect.Response[agentv1.RespondExecutionInteractionResponse], error) {
	owner, err := identity.FromContext(ctx)
	if err != nil {
		return nil, connect.NewError(connect.CodeUnauthenticated, err)
	}
	answers := make([]domain.ExecutionAnswer, 0, len(req.Msg.GetAnswers()))
	for _, a := range req.Msg.GetAnswers() {
		answers = append(answers, domain.ExecutionAnswer{QuestionID: a.GetQuestionId(), Selected: a.GetSelected(), Text: a.GetText()})
	}
	result, err := h.service.Respond(ctx, owner.UserID, req.Msg.GetInteractionId(), req.Msg.GetIdempotencyKey(), req.Msg.GetReject(), answers)
	if err != nil {
		return nil, interactionError(err)
	}
	return connect.NewResponse(&agentv1.RespondExecutionInteractionResponse{Interaction: interactionProto(result)}), nil
}
