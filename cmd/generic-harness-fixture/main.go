package main

import (
	"bufio"
	"encoding/json"
	"fmt"
	"os"

	"google.golang.org/protobuf/encoding/protojson"

	agentv1 "github.com/yangtao121/workos/gen/go/workos/agent/v1"
	"github.com/yangtao121/workos/internal/platform/ids"
)

func main() {
	var request struct {
		Version string          `json:"version"`
		TaskID  string          `json:"taskId"`
		Input   json.RawMessage `json:"input"`
	}
	if err := json.NewDecoder(bufio.NewReader(os.Stdin)).Decode(&request); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(2)
	}
	if request.Version != "workos.harness-cli/v1" || request.TaskID == "" {
		fmt.Fprintln(os.Stderr, "invalid WorkOS CLI envelope")
		os.Exit(2)
	}
	input := &agentv1.AgentTaskInput{}
	if err := protojson.Unmarshal(request.Input, input); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(2)
	}
	runID := ids.UUIDv7{}.New()
	events := []*agentv1.AgentEvent{
		{Event: &agentv1.AgentEvent_RunStarted{RunStarted: &agentv1.RunStarted{RunId: runID, ProviderId: "generic-cli"}}},
		{Event: &agentv1.AgentEvent_AssistantMessage{AssistantMessage: &agentv1.AssistantMessage{Text: input.GetGoal()}}},
		{Event: &agentv1.AgentEvent_RunCompleted{RunCompleted: &agentv1.RunCompleted{Summary: "generic CLI fixture completed"}}},
	}
	for _, event := range events {
		data, err := protojson.Marshal(event)
		if err != nil {
			fmt.Fprintln(os.Stderr, err)
			os.Exit(2)
		}
		fmt.Println(string(data))
	}
}
