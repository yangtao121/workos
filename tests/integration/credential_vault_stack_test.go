//go:build integration

package integration_test

import (
	"bufio"
	"context"
	"crypto/tls"
	"fmt"
	"net"
	"net/http"
	"os"
	"strings"
	"testing"
	"time"

	"connectrpc.com/connect"

	agentv1 "github.com/yangtao121/workos/gen/go/workos/agent/v1"
	"github.com/yangtao121/workos/gen/go/workos/agent/v1/agentv1connect"
	commonv1 "github.com/yangtao121/workos/gen/go/workos/common/v1"
	harnessv1 "github.com/yangtao121/workos/gen/go/workos/harness/v1"
	"github.com/yangtao121/workos/gen/go/workos/harness/v1/harnessv1connect"
	projectv1 "github.com/yangtao121/workos/gen/go/workos/project/v1"
	"github.com/yangtao121/workos/gen/go/workos/project/v1/projectv1connect"
)

// TestCredentialVaultStackPhase is the phase-driven stack gate driven by
// `make test-credential-vault` (ADR-0009). The environment variable selects
// the posture under test:
//
//   - missing: no active vault credential → DeepSeek is projected
//     unavailable to the owner and the binder refuses the selection.
//   - granted: an active credential exists (stored via the real workosctl
//     admin Unix socket) → binding carries a server-derived credential_ref,
//     and a full task runs on a task-bound lease through the local fixture.
//   - revoked: after revocation the provider is unavailable again and new
//     bindings are refused; existing facts keep their snapshots.
func TestCredentialVaultStackPhase(t *testing.T) {
	// This gate is driven exclusively by `make test-credential-vault`, which
	// sets the phase and brings up the DeepSeek fixture stack with the vault
	// provisioned through the admin socket. Any other context skips.
	phase := strings.TrimSpace(envOrDefault("WORKOS_TEST_VAULT_PHASE", ""))
	if phase == "" {
		t.Skip("WORKOS_TEST_VAULT_PHASE is not set: run make test-credential-vault")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	client := &http.Client{Transport: &http.Transport{Proxy: nil, DialContext: (&net.Dialer{Timeout: 2 * time.Second}).DialContext}}
	baseURL := "http://127.0.0.1:8080"
	projects := projectv1connect.NewProjectServiceClient(client, baseURL)
	bindings := projectv1connect.NewProjectHarnessBindingServiceClient(client, baseURL)
	tasks := agentv1connect.NewAgentTaskServiceClient(client, baseURL)
	catalogs := harnessv1connect.NewHarnessCatalogServiceClient(client, baseURL)

	assertExecutionListenerRejectsUnauthenticated(t)

	described, err := catalogs.GetHarnessCatalog(ctx, connect.NewRequest(&harnessv1.GetHarnessCatalogRequest{}))
	if err != nil {
		t.Fatalf("get catalog: %v", err)
	}
	deepseek := findProvider(described.Msg.GetProviders(), "deepseek")
	if deepseek == nil {
		t.Fatalf("deepseek provider missing from catalog: %#v", described.Msg.GetProviders())
	}

	key := fmt.Sprintf("vault-stack-%s-%d", phase, time.Now().UnixNano())
	created, err := projects.CreateProject(ctx, connect.NewRequest(&projectv1.CreateProjectRequest{IdempotencyKey: key, Name: "Vault Stack"}))
	if err != nil {
		t.Fatalf("create project: %v", err)
	}

	switch phase {
	case "missing", "revoked":
		if deepseek.GetHealth() == commonv1.HealthState_HEALTH_STATE_HEALTHY {
			t.Fatalf("deepseek must not be healthy without an active owner credential: %#v", deepseek)
		}
		if reason := deepseek.GetUnavailableReason(); reason == "" || strings.Contains(reason, "credential") && strings.Contains(reason, "workos-fixture") {
			t.Fatalf("unsafe unavailable reason: %q", reason)
		}
		_, bindErr := bindings.SetProjectHarnessBinding(ctx, connect.NewRequest(&projectv1.SetProjectHarnessBindingRequest{
			ProjectId: created.Msg.GetProject().GetId(), ExpectedRevision: created.Msg.GetProject().GetRevision(),
			Selection: &projectv1.SetProjectHarnessBindingRequest_ProviderId{ProviderId: "deepseek"},
		}))
		if bindErr == nil {
			t.Fatal("binding a credential-requiring provider succeeded without an active credential")
		}
	case "granted":
		if deepseek.GetHealth() != commonv1.HealthState_HEALTH_STATE_HEALTHY {
			t.Fatalf("deepseek must be healthy with an active credential: %#v", deepseek)
		}
		bound, err := bindings.SetProjectHarnessBinding(ctx, connect.NewRequest(&projectv1.SetProjectHarnessBindingRequest{
			ProjectId: created.Msg.GetProject().GetId(), ExpectedRevision: created.Msg.GetProject().GetRevision(),
			Selection: &projectv1.SetProjectHarnessBindingRequest_ProviderId{ProviderId: "deepseek"},
		}))
		if err != nil {
			t.Fatalf("bind deepseek: %v", err)
		}
		binding := bound.Msg.GetProject().GetHarnessBinding()
		if len(binding.GetCredentialRef()) != 36 {
			t.Fatalf("server-derived credential_ref missing: %#v", binding)
		}
		submitted, err := tasks.SubmitTask(ctx, connect.NewRequest(&agentv1.SubmitTaskRequest{
			IdempotencyKey: "task-" + key,
			Input: &agentv1.AgentTaskInput{
				TargetScope: &agentv1.TargetScope{Scope: &agentv1.TargetScope_ProjectId{ProjectId: created.Msg.GetProject().GetId()}},
				Role:        "general", Goal: "prove the DeepSeek project binding fixture",
				Budget: &agentv1.AgentBudget{MaxTokens: 2048, MaxRuntimeSeconds: 20},
			},
		}))
		if err != nil {
			t.Fatalf("submit vault task: %v", err)
		}
		stream, err := tasks.WatchTaskEvents(ctx, connect.NewRequest(&agentv1.WatchTaskEventsRequest{TaskId: submitted.Msg.GetTask().GetId()}))
		if err != nil {
			t.Fatalf("watch vault task: %v", err)
		}
		started, completed := false, false
		for stream.Receive() {
			event := stream.Msg().GetEvent()
			if event.GetRunStarted().GetProviderId() == "deepseek" {
				started = true
			}
			if event.GetRunCompleted() != nil {
				completed = true
			}
			if event.GetRunFailed() != nil {
				t.Fatalf("vault task failed: %#v", event.GetRunFailed())
			}
		}
		if err := stream.Err(); err != nil {
			t.Fatalf("vault task stream: %v", err)
		}
		if !started || !completed {
			t.Fatalf("vault task did not complete on a lease: started=%v completed=%v", started, completed)
		}
	default:
		t.Fatalf("unknown vault phase %q", phase)
	}
}

// assertExecutionListenerRejectsUnauthenticated proves the private execution
// listener is not an ordinary HTTP surface. Plain HTTP bytes never produce an
// HTTP response (at most a TLS alert record and connection close), and an
// unauthenticated TLS dial fails the handshake before any RPC runs.
func assertExecutionListenerRejectsUnauthenticated(t *testing.T) {
	t.Helper()
	conn, err := net.DialTimeout("tcp", "127.0.0.1:8086", 2*time.Second)
	if err != nil {
		t.Fatalf("execution listener unreachable: %v", err)
	}
	_, _ = conn.Write([]byte("POST /workos.taskexecution.v1.TaskExecutionService/ClaimTask HTTP/1.1\r\nHost: localhost\r\n\r\n"))
	conn.SetReadDeadline(time.Now().Add(3 * time.Second))
	buffer := make([]byte, 512)
	total := 0
	for total < len(buffer) {
		read, readErr := conn.Read(buffer[total:])
		total += read
		if readErr != nil {
			break
		}
	}
	conn.Close()
	answer := string(buffer[:total])
	// Go's http.Server answers a plain-HTTP request on a TLS port with one
	// fixed static 400 ("Client sent an HTTP request to an HTTPS server").
	// That exact refusal is the proof of a TLS-only port; any other HTTP
	// answer (routing, Connect errors, business JSON) would be a leak.
	const tlsOnlyRefusal = "Client sent an HTTP request to an HTTPS server"
	if !strings.Contains(answer, tlsOnlyRefusal) {
		t.Fatalf("execution listener answered plain HTTP with an unexpected response: %q", answer[:min(len(answer), 120)])
	}
	// TLS without a client certificate must never complete a request. Under
	// TLS 1.3 the client-side handshake may appear to succeed before the
	// server's certificate verdict arrives, so the verdict is checked with a
	// full round trip.
	certLess := &tls.Config{InsecureSkipVerify: true}
	if err := roundTripTLS(t, "127.0.0.1:8086", certLess); err == nil {
		t.Fatal("execution listener served a TLS connection without a client certificate")
	}
}

// roundTripTLS performs one minimal HTTP request over the given TLS
// configuration and reports any failure (handshake alert, read EOF, or
// non-200) as an error.
func roundTripTLS(t *testing.T, address string, config *tls.Config) error {
	t.Helper()
	conn, err := tls.DialWithDialer(&net.Dialer{Timeout: 3 * time.Second}, "tcp", address, config)
	if err != nil {
		return err
	}
	defer conn.Close()
	request, err := http.NewRequest(http.MethodGet, "https://"+address+"/healthz", nil)
	if err != nil {
		return err
	}
	if err := request.Write(conn); err != nil {
		return err
	}
	conn.SetReadDeadline(time.Now().Add(3 * time.Second))
	response, err := http.ReadResponse(bufio.NewReader(conn), request)
	if err != nil {
		return err
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		return fmt.Errorf("status %d", response.StatusCode)
	}
	return nil
}

func findProvider(providers []*harnessv1.HarnessProviderInfo, id string) *harnessv1.HarnessProviderInfo {
	for _, provider := range providers {
		if provider.GetId() == id {
			return provider
		}
	}
	return nil
}

func envOrDefault(key, fallback string) string {
	if value := strings.TrimSpace(os.Getenv(key)); value != "" {
		return value
	}
	return fallback
}
