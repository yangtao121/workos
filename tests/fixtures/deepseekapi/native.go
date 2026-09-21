package main

import (
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"time"
)

// nativeAutomation is deterministic model output only. Native goal/skill/child
// orchestration and all file operations still execute in the real processes.
func nativeAutomation(w http.ResponseWriter, r *http.Request, body []byte, goal string) bool {
	if !strings.Contains(goal, "V2_NATIVE_") {
		return false
	}
	var req chatRequest
	if json.Unmarshal(body, &req) != nil {
		http.Error(w, "bad request", 400)
		return true
	}
	emit := func(delta map[string]any, reason string) {
		data, _ := json.Marshal(map[string]any{"choices": []any{map[string]any{"delta": delta, "finish_reason": reason}}, "usage": map[string]any{"prompt_tokens": 20, "completion_tokens": 2}})
		writeSSE(w, []string{string(data), "[DONE]"})
	}
	tool := func(index int, name, id string, args map[string]any) any {
		data, _ := json.Marshal(args)
		return map[string]any{"index": index, "id": id, "type": "function", "function": map[string]any{"name": name, "arguments": string(data)}}
	}
	has := func(id string) bool {
		for _, m := range req.Messages {
			if m.Role == "tool" && m.ToolCallID == id {
				return true
			}
		}
		return false
	}
	for _, m := range req.Messages {
		if m.Role == "tool" && strings.Contains(fmt.Sprint(m.Content), "Error:") {
			http.Error(w, "native fixture operation failed", 400)
			return true
		}
	}
	if strings.Contains(goal, "V2_NATIVE_GOAL") {
		select {
		case <-r.Context().Done():
			return true
		case <-time.After(2 * time.Second):
		}
		emit(map[string]any{"role": "assistant", "content": "Native goal round checked the fixture."}, "stop")
	} else if strings.Contains(goal, "V2_NATIVE_CHILD_") {
		if strings.Contains(string(body), "PARENT_PRIVATE_CANARY") {
			http.Error(w, "child inherited parent history", 400)
			return true
		}
		name := "alpha"
		if strings.Contains(goal, "V2_NATIVE_CHILD_B") {
			name = "beta"
		}
		id := "native-child-" + name
		if !has(id) {
			command := "test -f README.md && test ! -f alpha.txt && test ! -f beta.txt && printf '" + name + " child result\\n' > " + name + ".txt && git status --porcelain && sleep 2"
			if strings.Contains(goal, "_HOLD") {
				command = "printf holding > held.txt; sleep 120"
			}
			emit(map[string]any{"role": "assistant", "content": nil, "tool_calls": []any{tool(0, "bash", id, map[string]any{"command": command, "description": "write isolated " + name + " result"})}}, "tool_calls")
		} else {
			emit(map[string]any{"role": "assistant", "content": name + " result is ready for review."}, "stop")
		}
	} else if strings.Contains(goal, "V2_NATIVE_DELEGATE") {
		suffix := ""
		if strings.Contains(goal, "_HOLD") {
			suffix = "_HOLD"
		}
		if !has("native-delegate-a") {
			emit(map[string]any{"role": "assistant", "content": nil, "tool_calls": []any{
				tool(0, "subagent", "native-delegate-a", map[string]any{"description": "Alpha isolated change", "prompt": "V2_NATIVE_CHILD_A" + suffix}),
				tool(1, "subagent", "native-delegate-b", map[string]any{"description": "Beta isolated change", "prompt": "V2_NATIVE_CHILD_B" + suffix}),
			}}, "tool_calls")
		} else {
			emit(map[string]any{"role": "assistant", "content": "Two isolated changes are ready for review."}, "stop")
		}
	} else if strings.Contains(goal, "V2_NATIVE_SKILL") {
		if !has("native-skill") {
			if !strings.Contains(string(body), "project-check") {
				http.Error(w, "missing project skill", 400)
				return true
			}
			emit(map[string]any{"role": "assistant", "content": nil, "tool_calls": []any{tool(0, "skill", "native-skill", map[string]any{"name": "project-check"})}}, "tool_calls")
		} else if !strings.Contains(string(body), "PROJECT_SKILL_PRIVATE_BODY") {
			http.Error(w, "skill body not loaded", 400)
		} else {
			emit(map[string]any{"role": "assistant", "content": "Project skill loaded through the native registry."}, "stop")
		}
	} else {
		http.Error(w, "unknown native fixture", 400)
	}
	return true
}
