package genericcli

import (
	"errors"
	"time"

	harnessv1 "github.com/yangtao121/workos/gen/go/workos/harness/v1"
	executionv1 "github.com/yangtao121/workos/gen/go/workos/taskexecution/v1"
	"github.com/yangtao121/workos/internal/harness/ports"
	"google.golang.org/protobuf/encoding/protojson"
)

const protocolVersion = "workos.harness-cli/v2"

func prepareRequest(execution ports.Execution, timeout time.Duration) ([]byte, time.Duration, map[string]bool, error) {
	if err := ports.ValidateRepairExecution(execution); err != nil {
		return nil, 0, nil, err
	}
	input := execution.Input
	invalid := func() ([]byte, time.Duration, map[string]bool, error) {
		return nil, 0, nil, ports.NewRunError(ports.ErrorKindInvalidInput, "generic CLI input is unsupported or inconsistent", false, nil)
	}
	if execution.TaskID == "" || input == nil || execution.Emit == nil || execution.Credential != nil || len(input.GetRequestedCapabilities()) != 0 {
		return invalid()
	}
	budget := input.GetBudget()
	if budget.GetMaxTokens() != 0 || budget.GetMaxCostDecimal() != "" || budget.GetMaxRuntimeSeconds() < 0 || budget.GetMaxRuntimeSeconds() > 86400 {
		return invalid()
	}
	if seconds := budget.GetMaxRuntimeSeconds(); seconds > 0 && time.Duration(seconds)*time.Second < timeout {
		timeout = time.Duration(seconds) * time.Second
	}
	requested := make(map[string]bool, len(input.GetOutputArtifactTypes()))
	for _, kind := range input.GetOutputArtifactTypes() {
		if kind != "document.markdown.v1" && kind != "code.unified-diff.v1" || requested[kind] {
			return invalid()
		}
		requested[kind] = true
	}
	if len(requested) > 0 && execution.ArtifactsBatch == nil && (len(requested) > 1 || execution.Artifacts == nil) {
		return invalid()
	}
	request := &harnessv1.HarnessCLIRequest{ProtocolVersion: protocolVersion, TaskId: execution.TaskID, Input: input, Repair: execution.Repair}
	if len(input.GetContextRefs()) != len(execution.Context) {
		return invalid()
	}
	for i, doc := range execution.Context {
		ref := input.GetContextRefs()[i]
		if ref.GetType() != "artifact.review.v1" || doc.RefType != ref.GetType() || doc.ArtifactID == "" || doc.ArtifactID != ref.GetId() || doc.Digest == "" || doc.Digest != ref.GetRevision() {
			return invalid()
		}
		if doc.ArtifactType != "document.markdown.v1" && doc.ArtifactType != "code.unified-diff.v1" {
			return invalid()
		}
		request.Context = append(request.Context, &executionv1.ResolvedTaskContextDocument{RefType: doc.RefType, ArtifactType: doc.ArtifactType, ArtifactId: doc.ArtifactID, Digest: doc.Digest, Title: doc.Title, MediaType: doc.MediaType, Content: doc.Content})
	}
	payload, err := protojson.Marshal(request)
	if err != nil || len(payload)+1 > maxRequestBytes {
		return nil, 0, nil, ports.NewRunError(ports.ErrorKindInvalidInput, "generic CLI request exceeds its protocol", false, nil)
	}
	return append(payload, '\n'), timeout, requested, nil
}

func decodeArtifact(artifact *executionv1.TaskArtifactOutput, requested map[string]bool, seen map[string]bool) (ports.ArtifactOutput, error) {
	output := ports.ArtifactOutput{Key: artifact.GetOutputKey(), Title: artifact.GetTitle()}
	switch content := artifact.GetContent().(type) {
	case *executionv1.TaskArtifactOutput_Markdown:
		output.Type = "document.markdown.v1"
		output.Content = content.Markdown.GetContent()
	case *executionv1.TaskArtifactOutput_UnifiedDiff:
		output.Type = "code.unified-diff.v1"
		output.Content = content.UnifiedDiff.GetContent()
	default:
		return ports.ArtifactOutput{}, errors.New("generic CLI artifact has no supported content")
	}
	if !requested[output.Type] || seen[output.Type] || output.Key == "" || output.Title == "" || len(output.Content) == 0 {
		return ports.ArtifactOutput{}, errors.New("generic CLI artifact was not requested or is incomplete")
	}
	seen[output.Type] = true
	return output, nil
}
