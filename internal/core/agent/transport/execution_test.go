package transport

import (
	"bytes"
	"context"
	"errors"
	"net/http/httptest"
	"testing"

	"connectrpc.com/connect"

	agentv1 "github.com/yangtao121/workos/gen/go/workos/agent/v1"
	artifactv1 "github.com/yangtao121/workos/gen/go/workos/artifact/v1"
	taskv1 "github.com/yangtao121/workos/gen/go/workos/taskexecution/v1"
	"github.com/yangtao121/workos/gen/go/workos/taskexecution/v1/taskexecutionv1connect"
	"github.com/yangtao121/workos/internal/core/agent/domain"
	artifactdomain "github.com/yangtao121/workos/internal/core/artifact/domain"
	artifactports "github.com/yangtao121/workos/internal/core/artifact/ports"
)

// fakeMaterializer records the single materialization call and answers with
// the canned Core-minted projection.
type fakeMaterializer struct {
	called    bool
	leaseID   string
	workerID  string
	outputKey string
	artType   string
	content   []byte
	failWith  error
}

func (f *fakeMaterializer) MaterializeTaskArtifact(_ context.Context, leaseID, workerID, outputKey, _ string, artifactType string, content []byte) (*artifactv1.Artifact, *agentv1.AgentEvent, error) {
	f.called, f.leaseID, f.workerID, f.outputKey, f.artType, f.content = true, leaseID, workerID, outputKey, artifactType, content
	if f.failWith != nil {
		return nil, nil, f.failWith
	}
	artifactID := "0198d7ea-0000-7000-8000-0000000000aa"
	return &artifactv1.Artifact{Id: artifactID, Type: artifactType, SourceTaskId: "0198d7ea-0000-7000-8000-0000000000bb"},
		&agentv1.AgentEvent{Id: artifactID, TaskId: "0198d7ea-0000-7000-8000-0000000000cc", Sequence: 4,
			Event: &agentv1.AgentEvent_ArtifactCreated{ArtifactCreated: &agentv1.ArtifactCreated{ArtifactId: artifactID, ArtifactType: artifactType}}},
		nil
}

func newExecutionServer(t *testing.T, materializer TaskArtifactMaterializer, options ...connect.ClientOption) taskexecutionv1connect.TaskExecutionServiceClient {
	t.Helper()
	_, handler := NewExecutionConnectHandler(nil, materializer, nil)
	server := httptest.NewServer(handler)
	t.Cleanup(server.Close)
	return taskexecutionv1connect.NewTaskExecutionServiceClient(server.Client(), server.URL, options...)
}

func TestAppendTaskEventRejectsProviderBuiltArtifactCreated(t *testing.T) {
	t.Parallel()
	materializer := &fakeMaterializer{}
	client := newExecutionServer(t, materializer)
	_, err := client.AppendTaskEvent(context.Background(), connect.NewRequest(&taskv1.AppendTaskEventRequest{
		LeaseId: "0198d7ea-0000-7000-8000-000000000001", WorkerId: "worker",
		Event: &agentv1.AgentEvent{Event: &agentv1.AgentEvent_ArtifactCreated{ArtifactCreated: &agentv1.ArtifactCreated{
			ArtifactId: "0198d7ea-0000-7000-8000-0000000000aa", ArtifactType: "document.markdown.v1",
		}}},
	}))
	connectErr := new(connect.Error)
	if !errors.As(err, &connectErr) || connectErr.Code() != connect.CodeInvalidArgument {
		t.Fatalf("provider-built ArtifactCreated must be rejected invalid, got %v", err)
	}
	if materializer.called {
		t.Fatal("rejection must not reach the materializer")
	}
}

func TestAppendTaskArtifactDerivesFactsFromTheLeaseOnly(t *testing.T) {
	t.Parallel()
	materializer := &fakeMaterializer{}
	client := newExecutionServer(t, materializer)
	response, err := client.AppendTaskArtifact(context.Background(), connect.NewRequest(&taskv1.AppendTaskArtifactRequest{
		LeaseId: "0198d7ea-0000-7000-8000-000000000001", WorkerId: "worker",
		Artifact: &taskv1.TaskArtifactOutput{
			OutputKey: "document", Title: "Title",
			Content: &taskv1.TaskArtifactOutput_Markdown{Markdown: &taskv1.MarkdownArtifactContent{Content: []byte("# hi\n")}},
		},
	}))
	if err != nil {
		t.Fatal(err)
	}
	if !materializer.called || materializer.leaseID != "0198d7ea-0000-7000-8000-000000000001" ||
		materializer.workerID != "worker" || materializer.outputKey != "document" ||
		materializer.artType != "document.markdown.v1" || string(materializer.content) != "# hi\n" {
		t.Fatalf("unexpected materialization call: %#v", materializer)
	}
	if response.Msg.GetArtifact().GetId() == "" || response.Msg.GetEvent().GetArtifactCreated().GetArtifactId() != response.Msg.GetArtifact().GetId() {
		t.Fatalf("unexpected Core-minted response: %#v", response.Msg)
	}
}

func TestAppendTaskArtifactRejectsUnknownContent(t *testing.T) {
	t.Parallel()
	materializer := &fakeMaterializer{}
	client := newExecutionServer(t, materializer)
	_, err := client.AppendTaskArtifact(context.Background(), connect.NewRequest(&taskv1.AppendTaskArtifactRequest{
		LeaseId: "0198d7ea-0000-7000-8000-000000000001", WorkerId: "worker",
		Artifact: &taskv1.TaskArtifactOutput{OutputKey: "x", Title: "y"},
	}))
	connectErr := new(connect.Error)
	if !errors.As(err, &connectErr) || connectErr.Code() != connect.CodeInvalidArgument {
		t.Fatalf("content-less output must be rejected, got %v", err)
	}
	if materializer.called {
		t.Fatal("invalid output must not reach the materializer")
	}
}

func TestAppendTaskArtifactWireBudgetPrecedesBusinessCode(t *testing.T) {
	t.Parallel()
	for name, options := range map[string][]connect.ClientOption{
		"protobuf":      nil,
		"protobuf gzip": {connect.WithSendGzip()},
		"json":          {connect.WithProtoJSON()},
		"json gzip":     {connect.WithProtoJSON(), connect.WithSendGzip()},
	} {
		t.Run(name, func(t *testing.T) {
			materializer := &fakeMaterializer{}
			client := newExecutionServer(t, materializer, options...)
			_, err := client.AppendTaskArtifact(context.Background(), connect.NewRequest(&taskv1.AppendTaskArtifactRequest{
				LeaseId: "0198d7ea-0000-7000-8000-000000000001", WorkerId: "worker",
				Artifact: &taskv1.TaskArtifactOutput{
					OutputKey: "document", Title: "Oversized",
					Content: &taskv1.TaskArtifactOutput_Markdown{Markdown: &taskv1.MarkdownArtifactContent{
						Content: bytes.Repeat([]byte("x"), MaxExecutionRequestBytes+1024),
					}},
				},
			}))
			if connect.CodeOf(err) != connect.CodeResourceExhausted {
				t.Fatalf("oversized private request must fail before decode, got %v", err)
			}
			if materializer.called {
				t.Fatal("materializer ran for an oversized private request")
			}
		})
	}
}

func TestAppendTaskArtifactWireBudgetAllowsMaximumCanonicalContent(t *testing.T) {
	t.Parallel()
	content := bytes.Repeat(
		append(bytes.Repeat([]byte("x"), artifactdomain.MaxReviewLineBytes-1), '\n'),
		artifactdomain.MaxReviewContentBytes/artifactdomain.MaxReviewLineBytes,
	)
	for name, options := range map[string][]connect.ClientOption{
		"protobuf": nil,
		"json":     {connect.WithProtoJSON()},
	} {
		t.Run(name, func(t *testing.T) {
			materializer := &fakeMaterializer{}
			client := newExecutionServer(t, materializer, options...)
			_, err := client.AppendTaskArtifact(context.Background(), connect.NewRequest(&taskv1.AppendTaskArtifactRequest{
				LeaseId: "0198d7ea-0000-7000-8000-000000000001", WorkerId: "worker",
				Artifact: &taskv1.TaskArtifactOutput{
					OutputKey: "document", Title: "Maximum",
					Content: &taskv1.TaskArtifactOutput_Markdown{Markdown: &taskv1.MarkdownArtifactContent{Content: content}},
				},
			}))
			if err != nil {
				t.Fatalf("maximum canonical content did not have wire headroom: %v", err)
			}
			if !materializer.called || len(materializer.content) != len(content) {
				t.Fatal("legal maximum request did not reach the materializer intact")
			}
		})
	}
}

func TestMaterializerErrorMatrix(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name string
		err  error
		want connect.Code
	}{
		{"lease lost", domain.ErrLeaseLost, connect.CodeAborted},
		{"terminal", domain.ErrTerminal, connect.CodeFailedPrecondition},
		{"output conflict", artifactdomain.ErrOutputConflict, connect.CodeFailedPrecondition},
		{"artifact store unavailable", artifactports.ErrStoreUnavailable, connect.CodeUnavailable},
		{"stored corruption", artifactdomain.ErrCorrupt, connect.CodeInternal},
		{"invalid", domain.ErrInvalid, connect.CodeInvalidArgument},
	}
	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			materializer := &fakeMaterializer{failWith: testCase.err}
			client := newExecutionServer(t, materializer)
			_, err := client.AppendTaskArtifact(context.Background(), connect.NewRequest(&taskv1.AppendTaskArtifactRequest{
				LeaseId: "0198d7ea-0000-7000-8000-000000000001", WorkerId: "worker",
				Artifact: &taskv1.TaskArtifactOutput{
					OutputKey: "document", Title: "Title",
					Content: &taskv1.TaskArtifactOutput_Markdown{Markdown: &taskv1.MarkdownArtifactContent{Content: []byte("x")}},
				},
			}))
			connectErr := new(connect.Error)
			if !errors.As(err, &connectErr) || connectErr.Code() != testCase.want {
				t.Fatalf("expected %v, got %v", testCase.want, err)
			}
		})
	}
}

func TestSubmitInputRejectsInvalidOutputTypes(t *testing.T) {
	t.Parallel()
	if err := validateOutputArtifactTypes([]string{"image.png.v1"}); err == nil {
		t.Fatal("non-canonical type accepted")
	}
	if err := validateOutputArtifactTypes([]string{"document.markdown.v1", "document.markdown.v1"}); err == nil {
		t.Fatal("duplicate type accepted")
	}
	if err := validateOutputArtifactTypes([]string{
		"document.markdown.v1", "code.unified-diff.v1", "document.markdown.v1",
	}); err == nil {
		t.Fatal("over-limit request accepted")
	}
	if err := validateOutputArtifactTypes([]string{"document.markdown.v1", "code.unified-diff.v1"}); err != nil {
		t.Fatalf("valid request rejected: %v", err)
	}
}
