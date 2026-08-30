package deepseek

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	agentv1 "github.com/yangtao121/workos/gen/go/workos/agent/v1"
	commonv1 "github.com/yangtao121/workos/gen/go/workos/common/v1"
	harnessv1 "github.com/yangtao121/workos/gen/go/workos/harness/v1"
	"github.com/yangtao121/workos/internal/harness/ports"
	"github.com/yangtao121/workos/internal/platform/ids"
)

type Provider struct {
	config Config
	ids    ids.Generator

	mu     sync.RWMutex
	health commonv1.HealthState
	reason string
}

type preparedInput struct {
	goal string
	// envelope is the versioned canonical task envelope handed to the
	// runtime as the single user content block when the task carries pinned
	// context. Empty means a context-free run keeps the plain goal text.
	envelope  string
	maxTokens int64
	timeout   time.Duration
}

func New(config Config, generator ids.Generator) *Provider {
	config = normalizeConfig(config)
	provider := &Provider{config: config, ids: generator}
	if err := validateConfig(config); err != nil {
		provider.health = commonv1.HealthState_HEALTH_STATE_UNAVAILABLE
		provider.reason = err.Error()
	} else {
		provider.health = commonv1.HealthState_HEALTH_STATE_HEALTHY
	}
	return provider
}

func (p *Provider) Describe() *harnessv1.HarnessProviderInfo {
	p.mu.RLock()
	health, reason := p.health, p.reason
	p.mu.RUnlock()
	return &harnessv1.HarnessProviderInfo{
		Id:                ProviderID,
		DisplayName:       "DeepSeek Harness",
		AdapterVersion:    AdapterVersion,
		Health:            health,
		UnavailableReason: reason,
		Capabilities: &harnessv1.HarnessCapabilities{
			Streaming:      true,
			UsageReporting: true,
			// The pinned runtime enforces max_tokens as a real provider cap
			// and the adapter maps max_runtime_seconds onto a hard process
			// deadline; tests prove both contracts (ADR-0005).
			HardTokenBudget:     true,
			HardRuntimeDeadline: true,
			// The enforced maxima prepareInput refuses budgets beyond, so Core
			// can reject over-bound policies before queueing or reserving.
			MaxOutputTokens:   MaximumMaxTokens,
			MaxRuntimeSeconds: int64(MaximumTimeout / time.Second),
			// The adapter never holds a long-lived API key: every run needs a
			// short-lived, task-bound credential lease from the Core
			// Credential Vault (ADR-0009).
			RequiresTaskCredentialLease: true,
			// Proven only after the materialized-context tests pass: the
			// adapter consumes review artifacts as bounded untrusted context
			// through the versioned task envelope (ADR-0010).
			SupportedContextRefTypes: supportedContextRefTypes,
		},
	}
}

// Run keeps structured artifact support honestly unsupported: the sink is
// ignored and prepareInput refuses any requested artifact type (ADR-0008).
// Context references stay unsupported until their materialization protocol
// exists; the credential lease, by contrast, is required on every run —
// without a live lease matching this provider and purpose the run fails
// closed before any child process starts.
func (p *Provider) Run(ctx context.Context, execution ports.Execution) error {
	taskID, input, emit := execution.TaskID, execution.Input, execution.Emit
	_ = execution.Artifacts

	if err := validateConfig(p.config); err != nil {
		p.setHealth(commonv1.HealthState_HEALTH_STATE_UNAVAILABLE, err.Error())
		return ports.NewRunError(ports.ErrorKindConfiguration, err.Error(), false, nil)
	}
	now := time.Now()
	if !execution.Credential.ValidFor(ProviderID, ports.PurposeProviderAPIKeyV1, now) {
		return ports.NewRunError(ports.ErrorKindConfiguration, "provider credential lease is missing, expired, or bound to another provider", false, nil)
	}
	lease := execution.Credential
	// The secret exists only inside this run's child environment and only
	// for this task. The lease buffer is overwritten best-effort when the
	// run ends; Go cannot formally zeroize exec/string copies (ADR-0009).
	defer func() {
		for index := range lease.Secret {
			lease.Secret[index] = 0
		}
	}()
	prepared, err := prepareInput(input, execution.Context, p.config.Timeout)
	if err != nil {
		return err
	}
	runID := p.ids.New()
	err = p.execute(ctx, taskID, runID, prepared, lease, emit)
	if err == nil {
		p.setHealth(commonv1.HealthState_HEALTH_STATE_HEALTHY, "")
		return nil
	}
	if errors.Is(err, context.Canceled) {
		return err
	}
	var runErr *ports.RunError
	if errors.As(err, &runErr) {
		switch runErr.Kind {
		case ports.ErrorKindAuthentication, ports.ErrorKindConfiguration:
			p.setHealth(commonv1.HealthState_HEALTH_STATE_UNAVAILABLE, runErr.Error())
		case ports.ErrorKindRateLimit, ports.ErrorKindProvider, ports.ErrorKindTransport, ports.ErrorKindTimeout:
			p.setHealth(commonv1.HealthState_HEALTH_STATE_DEGRADED, runErr.Error())
		}
	}
	return err
}

func (p *Provider) setHealth(health commonv1.HealthState, reason string) {
	p.mu.Lock()
	p.health, p.reason = health, reason
	p.mu.Unlock()
}

// supportedContextRefTypes is exact: the adapter demonstrably consumes
// review artifacts as resolved bounded context (ADR-0010).
var supportedContextRefTypes = []string{"artifact.review.v1"}

const (
	// taskEnvelopeVersion pins the canonical user-content envelope.
	taskEnvelopeVersion = "workos.deepseek.task-envelope.v1"
	// maximumContextDocuments matches the canonical per-task ref bound.
	maximumContextDocuments = 4
	// maximumContextDocumentBytes is the per-document content bound.
	maximumContextDocumentBytes = 512 * 1024
)

// reviewArtifactType validates the artifact type of one resolved context
// document against the canonical review vocabulary.
func reviewArtifactType(value string) (string, string, bool) {
	switch value {
	case "document.markdown.v1":
		return value, "text/markdown; charset=utf-8", true
	case "code.unified-diff.v1":
		return value, "text/x-diff; charset=utf-8", true
	default:
		return "", "", false
	}
}

func prepareInput(input *agentv1.AgentTaskInput, contexts []ports.ContextDocument, configuredTimeout time.Duration) (preparedInput, error) {
	if input == nil {
		return preparedInput{}, invalidInput("DeepSeek task input is required")
	}
	if strings.TrimSpace(input.GetGoal()) == "" {
		return preparedInput{}, invalidInput("DeepSeek task goal is required")
	}
	if len(input.GetGoal()) > maximumGoalBytes {
		return preparedInput{}, invalidInput("DeepSeek task goal exceeds the supported size")
	}
	role := input.GetRole()
	if role != "" && role != "general" {
		return preparedInput{}, invalidInput("DeepSeek Harness supports only the general role")
	}
	pinnedRefs := input.GetContextRefs()
	if len(pinnedRefs) > maximumContextDocuments {
		return preparedInput{}, invalidInput("DeepSeek Harness context exceeds the supported size")
	}
	if len(input.GetRequestedCapabilities()) != 0 {
		return preparedInput{}, invalidInput("DeepSeek Harness does not support requested capabilities")
	}
	if len(input.GetOutputArtifactTypes()) != 0 {
		return preparedInput{}, invalidInput("DeepSeek Harness does not support structured artifacts")
	}

	maxTokens, timeout := DefaultMaxTokens, configuredTimeout
	if budget := input.GetBudget(); budget != nil {
		if budget.GetMaxCostDecimal() != "" {
			return preparedInput{}, invalidInput("DeepSeek Harness does not support cost budgets")
		}
		if budget.GetMaxTokens() < 0 || budget.GetMaxTokens() > MaximumMaxTokens {
			return preparedInput{}, invalidInput(fmt.Sprintf("DeepSeek max_tokens must be between 1 and %d", MaximumMaxTokens))
		}
		if budget.GetMaxTokens() > 0 {
			maxTokens = budget.GetMaxTokens()
		}
		if budget.GetMaxRuntimeSeconds() < 0 || budget.GetMaxRuntimeSeconds() > int64(MaximumTimeout/time.Second) {
			return preparedInput{}, invalidInput("DeepSeek max_runtime_seconds must be between 1 and 600")
		}
		if budget.GetMaxRuntimeSeconds() > 0 {
			requested := time.Duration(budget.GetMaxRuntimeSeconds()) * time.Second
			if requested < timeout {
				timeout = requested
			}
		}
	}
	goal := input.GetGoal()
	envelope := ""
	if len(pinnedRefs) != 0 {
		// The versioned canonical envelope (ADR-0010): the goal and every
		// resolved document travel as one structured user content block with
		// explicit untrusted_context semantics. Context bytes can never be
		// promoted to a trusted prompt by delimiter collision, and the
		// adapter validates count, order, digests, UTF-8, and bounds before
		// any child process starts.
		if len(contexts) != len(pinnedRefs) {
			return preparedInput{}, invalidInput("resolved context count does not match the pinned references")
		}
		untrusted := make([]map[string]string, 0, len(contexts))
		for index, document := range contexts {
			ref := pinnedRefs[index]
			if document.RefType != ref.GetType() || document.ArtifactID != ref.GetId() || document.Digest != ref.GetRevision() {
				return preparedInput{}, invalidInput("resolved context does not match the pinned reference")
			}
			if _, _, ok := reviewArtifactType(document.ArtifactType); !ok {
				return preparedInput{}, invalidInput("resolved context type is not a supported review artifact")
			}
			if len(document.Content) == 0 || len(document.Content) > maximumContextDocumentBytes || !utf8.Valid(document.Content) {
				return preparedInput{}, invalidInput("resolved context content is invalid")
			}
			untrusted = append(untrusted, map[string]string{
				"artifactType": document.ArtifactType,
				"digest":       document.Digest,
				"mediaType":    document.MediaType,
				"refType":      document.RefType,
				"title":        document.Title,
				"bytesBase64":  base64.StdEncoding.EncodeToString(document.Content),
			})
		}
		encoded, err := json.Marshal(map[string]any{
			"version":            taskEnvelopeVersion,
			"goal":               goal,
			"untrusted_contexts": untrusted,
		})
		if err != nil {
			return preparedInput{}, invalidInput("task envelope encoding failed")
		}
		envelope = string(encoded)
	}
	return preparedInput{goal: goal, envelope: envelope, maxTokens: maxTokens, timeout: timeout}, nil
}

func invalidInput(reason string) error {
	return ports.NewRunError(ports.ErrorKindInvalidInput, reason, false, nil)
}
