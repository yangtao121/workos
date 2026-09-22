package deepseek

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	agentv1 "github.com/yangtao121/workos/gen/go/workos/agent/v1"
)

func TestNativeProjectSkillsRegistryAndHumanInvocation(t *testing.T) {
	runtime := os.Getenv("WORKOS_DEEPSEEK_RUNTIME_PROBE")
	if runtime == "" {
		t.Skip("set WORKOS_DEEPSEEK_RUNTIME_PROBE to the pinned runtime")
	}
	plugin, err := filepath.Abs("../../../../deploy/harness/workos-tools.mjs")
	if err != nil {
		t.Fatal(err)
	}
	var requests atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(io.LimitReader(r.Body, 2*1024*1024))
		step := requests.Add(1)
		source := string(body)
		if r.URL.Path != "/chat/completions" || !strings.Contains(source, "project-fixture") || strings.Contains(source, "SHADOW_BODY") || strings.Contains(source, "HOME_ONLY_BODY") {
			t.Error("incorrect project skill catalog scope")
			http.Error(w, "bad scope", 400)
			return
		}
		w.Header().Set("Content-Type", "text/event-stream")
		if step == 1 {
			if strings.Contains(source, "PROJECT_BODY") || strings.Contains(source, "PRIVATE_BODY") {
				t.Error("skill bodies loaded before invocation")
			}
			fmt.Fprint(w, "data: {\"choices\":[{\"delta\":{\"role\":\"assistant\",\"content\":null,\"tool_calls\":[{\"index\":0,\"id\":\"skill-call\",\"type\":\"function\",\"function\":{\"name\":\"skill\",\"arguments\":\"{\\\"name\\\":\\\"project-fixture\\\"}\"}}]}}]}\n\ndata: {\"choices\":[{\"delta\":{},\"finish_reason\":\"tool_calls\"}],\"usage\":{\"prompt_tokens\":10,\"completion_tokens\":2}}\n\ndata: [DONE]\n\n")
			return
		}
		if !strings.Contains(source, "PROJECT_BODY") || (step == 3 && !strings.Contains(source, "PRIVATE_BODY")) {
			t.Error("native skill instructions missing")
		}
		fmt.Fprint(w, "data: {\"choices\":[{\"delta\":{\"role\":\"assistant\",\"content\":\"Skill fixture done\"},\"finish_reason\":\"stop\"}],\"usage\":{\"prompt_tokens\":12,\"completion_tokens\":2}}\n\ndata: [DONE]\n\n")
	}))
	defer server.Close()
	manager := NewSessionManager(normalizeConfig(Config{Enabled: true, Environment: "test", RuntimePath: runtime, BaseURL: server.URL, Model: "deepseek-v4-flash", WorkosToolsPath: plugin, Timeout: 30 * time.Second}), nil)
	defer manager.Shutdown()
	root := t.TempDir()
	proc, err := manager.Ensure(context.Background(), "native-skills", "", root, []byte("native-fixture-key"), "", "")
	if err != nil {
		t.Fatal(err)
	}
	// A readable host-home skill must never enter the project-only provider.
	homeSkill := filepath.Join(sessionHomeDir(filepath.Join(root, "native-skills")), ".agents", "skills", "home")
	if err := os.MkdirAll(homeSkill, 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(homeSkill, "SKILL.md"), []byte("---\nname: home-only\ndescription: home-only\n---\nHOME_ONLY_BODY"), 0600); err != nil {
		t.Fatal(err)
	}
	files := map[string]string{
		"/workspace/.dsh/skills/project/SKILL.md":   "---\nname: project-fixture\ndescription: |\n  Verify project fixture instructions.\n---\nPROJECT_BODY",
		"/workspace/.dsh/skills/private/SKILL.md":   "---\nname: private-fixture\ndescription: Explicit user invocation only.\ndisable-model-invocation: true\n---\nPRIVATE_BODY",
		"/workspace/.agents/skills/shadow/SKILL.md": "---\nname: project-fixture\ndescription: Lower priority duplicate.\n---\nSHADOW_BODY",
	}
	proc.tools = func(_ context.Context, operation string, args map[string]any) (map[string]any, error) {
		path, _ := args["path"].(string)
		if !strings.HasPrefix(path, "/workspace/") && path != "/workspace" {
			return nil, fmt.Errorf("outside fixture workspace")
		}
		switch operation {
		case "fs.resolve":
			return map[string]any{"displayPath": path}, nil
		case "fs.stat":
			if _, ok := files[path]; ok {
				return map[string]any{"type": "file"}, nil
			}
			return map[string]any{"absent": true}, nil
		case "fs.list":
			names := []string{}
			if path == "/workspace/.dsh/skills" {
				names = []string{"private", "project"}
			}
			if path == "/workspace/.agents/skills" {
				names = []string{"shadow"}
			}
			entries := []any{}
			for _, name := range names {
				entries = append(entries, map[string]any{"name": name, "type": "directory", "target": map[string]any{"displayPath": path + "/" + name}})
			}
			return map[string]any{"entries": entries}, nil
		case "fs.read":
			if content, ok := files[path]; ok {
				return map[string]any{"content": content}, nil
			}
		}
		return nil, fmt.Errorf("unexpected fixture operation")
	}
	loaded := false
	emit := func(event *agentv1.AgentEvent) error {
		if output := event.GetToolCallCompleted(); output != nil {
			payload, _ := json.Marshal(output.Output)
			loaded = loaded || strings.Contains(string(payload), "PROJECT_BODY")
		}
		return nil
	}
	if err := manager.Prompt(context.Background(), proc, "load-project", "Use project-fixture", 32, 20*time.Second, emit); err != nil {
		t.Fatal(err)
	}
	if !loaded {
		t.Fatal("native skill tool did not load project instructions")
	}
	if err := manager.Prompt(context.Background(), proc, "invoke-private", "/private-fixture\nFollow these project instructions.", 32, 20*time.Second, emit); err != nil {
		t.Fatal(err)
	}
	if requests.Load() != 3 {
		t.Fatalf("model requests=%d", requests.Load())
	}
}
