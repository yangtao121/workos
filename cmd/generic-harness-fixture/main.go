package main

import (
	"bytes"
	"fmt"
	"io"
	"os"

	agentv1 "github.com/yangtao121/workos/gen/go/workos/agent/v1"
	appv1 "github.com/yangtao121/workos/gen/go/workos/app/v1"
	harnessv1 "github.com/yangtao121/workos/gen/go/workos/harness/v1"
	executionv1 "github.com/yangtao121/workos/gen/go/workos/taskexecution/v1"
	"github.com/yangtao121/workos/internal/platform/ids"
	"google.golang.org/protobuf/encoding/protojson"
	"google.golang.org/protobuf/proto"
)

func main() {
	body, err := io.ReadAll(io.LimitReader(os.Stdin, (1<<20)+1))
	request := &harnessv1.HarnessCLIRequest{}
	if err != nil || len(body) > 1<<20 || protojson.Unmarshal(body, request) != nil || request.GetProtocolVersion() != "workos.harness-cli/v2" || request.GetTaskId() == "" || request.GetInput() == nil {
		fail()
	}
	emit := func(event *agentv1.AgentEvent) {
		write(&harnessv1.HarnessCLIResponse{Payload: &harnessv1.HarnessCLIResponse_Event{Event: event}})
	}
	emit(&agentv1.AgentEvent{Event: &agentv1.AgentEvent_RunStarted{RunStarted: &agentv1.RunStarted{RunId: ids.UUIDv7{}.New(), ProviderId: "generic-cli"}}})
	input := request.GetInput()
	if len(input.GetOutputArtifactTypes()) == 0 {
		emit(&agentv1.AgentEvent{Event: &agentv1.AgentEvent_AssistantMessage{AssistantMessage: &agentv1.AssistantMessage{Text: input.GetGoal()}}})
	}
	for _, kind := range input.GetOutputArtifactTypes() {
		artifact := &executionv1.TaskArtifactOutput{OutputKey: kind, Title: "Generic CLI fixture review"}
		switch kind {
		case "document.markdown.v1":
			content := "# Generic CLI review\n\nDeterministic fixture output.\n"
			for _, doc := range request.GetContext() {
				content += "\nContext: " + doc.GetArtifactId() + " at " + doc.GetDigest() + "\n" + string(doc.GetContent()) + "\n"
			}
			artifact.Content = &executionv1.TaskArtifactOutput_Markdown{Markdown: &executionv1.MarkdownArtifactContent{Content: []byte(content)}}
		case "code.unified-diff.v1":
			artifact.Content = &executionv1.TaskArtifactOutput_UnifiedDiff{UnifiedDiff: &executionv1.UnifiedDiffArtifactContent{Content: []byte("--- a/fixture.txt\n+++ b/fixture.txt\n@@ -1 +1 @@\n-before\n+after\n")}}
		default:
			fail()
		}
		write(&harnessv1.HarnessCLIResponse{Payload: &harnessv1.HarnessCLIResponse_Artifact{Artifact: artifact}})
	}
	if repair := request.GetRepair(); repair != nil {
		output := &executionv1.RepairSourceOutput{}
		changed := false
		for _, file := range repair.GetSource().GetFiles() {
			next := proto.Clone(file).(*appv1.AppSourceFile)
			if next.Path == "main.go" && bytes.Contains(next.Content, []byte("return 0")) {
				next.Content = bytes.ReplaceAll(next.Content, []byte("return 0"), []byte("return 42"))
				changed = true
			}
			output.Files = append(output.Files, next)
		}
		if !changed {
			fail()
		}
		write(&harnessv1.HarnessCLIResponse{Payload: &harnessv1.HarnessCLIResponse_RepairSource{RepairSource: output}})
	}
	emit(&agentv1.AgentEvent{Event: &agentv1.AgentEvent_RunCompleted{RunCompleted: &agentv1.RunCompleted{Summary: "Generic CLI fixture completed"}}})
}
func write(response *harnessv1.HarnessCLIResponse) {
	body, err := protojson.Marshal(response)
	if err != nil {
		fail()
	}
	fmt.Println(string(body))
}
func fail() { fmt.Fprintln(os.Stderr, "invalid Generic CLI fixture protocol"); os.Exit(2) }
