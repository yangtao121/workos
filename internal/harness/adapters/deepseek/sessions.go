package deepseek

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"syscall"
	"time"

	agentv1 "github.com/yangtao121/workos/gen/go/workos/agent/v1"
	"github.com/yangtao121/workos/internal/harness/ports"
)

const (
	// sessionEnvMaxTokens is the fixed child-side turn budget the official
	// runtime reads from its environment; per-task budgets are enforced by
	// the single-shot path while continuous sessions use the pinned default.
	sessionEnvMaxTokens = int64(8192)
	// sessionShutdownWait bounds the graceful shutdown exchange before the
	// process group is killed.
	sessionShutdownWait = 3 * time.Second
	// maximumPendingNotifications bounds the buffered notifications that
	// arrive while the initialize handshake is still in flight.
	maximumPendingNotifications = 1024
)

// SessionManager manages the credential-bearing child for one execution.
// Native persistence owns continuity across children; the provider closes
// each child when its task ends.
type SessionManager struct {
	mu        sync.Mutex
	config    Config
	processes map[string]*sessionProcess
	logger    *slog.Logger
	now       func() time.Time
}

// sessionProcess is one live native runtime child. mu serializes prompts;
// pending holds notifications that arrived while the initialize handshake was
// still running and are replayed to the first prompt reader.
type sessionProcess struct {
	tools          ports.ToolCall
	sessionID      string
	workspaceRoot  string
	ownerUserID    string
	projectID      string
	keyFingerprint string
	cmd            *exec.Cmd
	stdin          io.WriteCloser
	reader         *frameReader
	pending        []rpcEnvelope
	nextID         int64
	mu             sync.Mutex
	done           chan struct{}
	terminateOnce  sync.Once
	lastUsed       atomic.Int64
}

func NewSessionManager(config Config, logger *slog.Logger) *SessionManager {
	if logger == nil {
		logger = slog.Default()
	}
	return &SessionManager{config: config, processes: make(map[string]*sessionProcess), logger: logger, now: time.Now}
}

// Ensure returns the live process for the session, spawning one when needed.
// An existing process is reused only when the child is alive, the credential
// fingerprint matches, and the workspace binding is unchanged; anything else
// kills the old process group and starts fresh. ownerUserID and projectID are
// the server-derived session authorization facts (ADR-0030); they enter only
// the child's WorkOS tool environment and are never reused across a different
// owner/project pair — a mismatch respawns the child like a rotation would.
func (m *SessionManager) Ensure(ctx context.Context, sessionID, workspaceRoot, stateRoot string, secret []byte, ownerUserID, projectID string) (*sessionProcess, error) {
	if !safeSessionComponent(sessionID) {
		return nil, ports.NewRunError(ports.ErrorKindInvalidInput, "DeepSeek session id is not a safe identifier", false, nil)
	}
	if workspaceRoot != "" && !filepath.IsAbs(workspaceRoot) {
		return nil, ports.NewRunError(ports.ErrorKindInvalidInput, "DeepSeek session workspace root must be absolute", false, nil)
	}
	if stateRoot == "" || !filepath.IsAbs(stateRoot) {
		return nil, ports.NewRunError(ports.ErrorKindConfiguration, "DeepSeek session state root is required and must be absolute", false, nil)
	}
	if err := ensureSessionDirectory(stateRoot); err != nil {
		return nil, err
	}
	stateDir := filepath.Join(stateRoot, sessionID)
	for _, dir := range []string{stateDir, sessionHomeDir(stateDir), sessionStateDir(stateDir), sessionWorkspaceDir(stateDir), sessionPersistenceDir(stateDir)} {
		if err := ensureSessionDirectory(dir); err != nil {
			return nil, err
		}
	}
	workspace := workspaceRoot
	if workspace == "" {
		workspace = sessionWorkspaceDir(stateDir)
	}
	fingerprint := credentialFingerprint(secret)

	m.mu.Lock()
	defer m.mu.Unlock()
	if existing := m.processes[sessionID]; existing != nil {
		if processAlive(existing) && existing.keyFingerprint == fingerprint && existing.workspaceRoot == workspaceRoot &&
			existing.ownerUserID == ownerUserID && existing.projectID == projectID {
			return existing, nil
		}
		m.terminate(existing)
		delete(m.processes, sessionID)
	}
	proc, err := m.spawn(ctx, sessionID, workspaceRoot, stateDir, workspace, fingerprint, secret, ownerUserID, projectID)
	if err != nil {
		return nil, err
	}
	m.processes[sessionID] = proc
	m.logger.Debug("deepseek session process spawned", "session_id", sessionID)
	return proc, nil
}

// Prompt runs exactly one native turn on the session process and maps the
// official event stream onto canonical AgentEvents. RunStarted is emitted
// before the prompt is written; the turn ends after turn/end plus the idle
// status. A deadline or cancellation kills the whole process group — the
// native context cannot survive a partial turn — and the run fails.
func (m *SessionManager) Prompt(ctx context.Context, proc *sessionProcess, runID, text string, maxTokens int64, timeout time.Duration, emit ports.Emit) error {
	if maxTokens <= 0 || maxTokens > MaximumMaxTokens {
		return ports.NewRunError(ports.ErrorKindInvalidInput, "DeepSeek session max_tokens must be between 1 and 384000", false, nil)
	}
	if timeout <= 0 {
		timeout = m.config.Timeout
	}
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	stop := m.watchdog(proc, ctx)
	defer stop()

	proc.mu.Lock()
	defer proc.mu.Unlock()
	proc.lastUsed.Store(m.now().UnixNano())
	if err := emit(&agentv1.AgentEvent{Event: &agentv1.AgentEvent_RunStarted{RunStarted: &agentv1.RunStarted{
		RunId: runID, ProviderId: ProviderID,
	}}}); err != nil {
		return err
	}
	requestID := proc.nextID + 1
	proc.nextID = requestID
	if err := writeRequest(ctx, proc.stdin, requestID, "session/prompt", map[string]any{
		"sessionId":     proc.sessionID,
		"contentBlocks": []map[string]string{{"type": "text", "text": text}},
		"messageId":     runID,
		"maxTokens":     maxTokens,
	}); err != nil {
		return m.fail(proc, processError(ctx, err))
	}
	mapper := &sessionEventMapper{sessionID: proc.sessionID, model: m.config.Model, emit: emit, usages: make(map[string]tokenUsage)}
	responded := false
	for !(responded && mapper.turnEnded && mapper.idle) {
		envelope, err := m.nextFrame(ctx, proc)
		if err != nil {
			return m.fail(proc, processError(ctx, err))
		}
		if envelope.Method == "workos/tool" && len(envelope.ID) != 0 {
			if err := m.handleTool(ctx, proc, envelope); err != nil {
				return m.fail(proc, err)
			}
			continue
		}
		if len(envelope.ID) != 0 {
			id, idErr := responseID(envelope)
			if idErr != nil || id != requestID || responded {
				return m.fail(proc, protocolError("DeepSeek Harness returned an unexpected response", idErr))
			}
			if envelope.Error != nil {
				return m.fail(proc, classifyRPCError(envelope.Error))
			}
			var result promptResult
			if err := json.Unmarshal(envelope.Result, &result); err != nil || result.MessageID == "" {
				return m.fail(proc, protocolError("DeepSeek Harness prompt response is malformed", err))
			}
			responded = true
			continue
		}
		if err := mapper.handleNotification(envelope); err != nil {
			return m.fail(proc, err)
		}
	}
	if err := mapper.finishTurn(); err != nil {
		return m.fail(proc, err)
	}
	return mapper.emitTurnSummary()
}

// Close terminates one session: best-effort shutdown RPC bounded by
// sessionShutdownWait, then a process-group kill.
func (m *SessionManager) Close(sessionID string) {
	m.mu.Lock()
	proc, ok := m.processes[sessionID]
	if ok {
		delete(m.processes, sessionID)
	}
	m.mu.Unlock()
	if ok {
		m.closeProcess(proc)
	}
}

// Shutdown terminates every live session process. The harness host wires
// this into its lifecycle; until then it stays exported for that wiring.
func (m *SessionManager) Shutdown() {
	m.mu.Lock()
	processes := make([]*sessionProcess, 0, len(m.processes))
	for sessionID, proc := range m.processes {
		processes = append(processes, proc)
		delete(m.processes, sessionID)
	}
	m.mu.Unlock()
	for _, proc := range processes {
		m.closeProcess(proc)
	}
}

// SweepIdle kills processes not prompted within maxIdle. An idle process has
// no in-flight turn, so the sweep skips the graceful shutdown exchange.
func (m *SessionManager) SweepIdle(maxIdle time.Duration) {
	if maxIdle <= 0 {
		return
	}
	m.mu.Lock()
	stale := make([]*sessionProcess, 0)
	for sessionID, proc := range m.processes {
		if m.now().Sub(time.Unix(0, proc.lastUsed.Load())) > maxIdle {
			stale = append(stale, proc)
			delete(m.processes, sessionID)
		}
	}
	m.mu.Unlock()
	for _, proc := range stale {
		m.terminate(proc)
	}
}

// spawn starts the pinned runtime child for one session and completes the
// initialize handshake before the process is published. The generated cordis
// composition lives under the harness-host-private state directory; the
// configured CordisConfigPath is deliberately ignored for sessions.
func (m *SessionManager) spawn(ctx context.Context, sessionID, workspaceRoot, stateDir, workspace, fingerprint string, secret []byte, ownerUserID, projectID string) (*sessionProcess, error) {
	// The read-only WorkOS tools plugin (B04) is loaded as a
	// configuration-relative row: the pinned runtime resolves './workos-tools.mjs'
	// against the cordis.yml directory, so the image file is copied into this
	// session's private state directory first. A missing plugin file fails the
	// spawn closed — the tools never silently disappear from a session.
	pluginSource, err := os.ReadFile(m.config.WorkosToolsPath)
	if err != nil {
		return nil, ports.NewRunError(ports.ErrorKindConfiguration, "DeepSeek session WorkOS tools plugin is unavailable", false, err)
	}
	if err := os.WriteFile(filepath.Join(stateDir, workosToolsFileName), pluginSource, 0o600); err != nil {
		return nil, ports.NewRunError(ports.ErrorKindUnavailable, "DeepSeek session state directory is not writable", true, err)
	}
	workspacePlugin, err := os.ReadFile(filepath.Join(filepath.Dir(m.config.WorkosToolsPath), "workos-workspace.mjs"))
	if err != nil {
		return nil, ports.NewRunError(ports.ErrorKindConfiguration, "Workspace backend plugin unavailable", false, err)
	}
	if err := os.WriteFile(filepath.Join(stateDir, "workos-workspace.mjs"), workspacePlugin, 0600); err != nil {
		return nil, err
	}
	bridge, err := os.ReadFile(filepath.Join(filepath.Dir(m.config.WorkosToolsPath), "workos-session.mjs"))
	if err != nil {
		return nil, ports.NewRunError(ports.ErrorKindConfiguration, "DeepSeek session bridge is unavailable", false, err)
	}
	if err := os.WriteFile(filepath.Join(stateDir, "workos-session.mjs"), bridge, 0o600); err != nil {
		return nil, err
	}
	cordisPath := filepath.Join(stateDir, "cordis.yml")
	if err := os.WriteFile(cordisPath, renderCordisConfig(stateDir, workspace, m.config), 0o600); err != nil {
		return nil, ports.NewRunError(ports.ErrorKindUnavailable, "DeepSeek session state directory is not writable", true, err)
	}
	// Runtime stderr is captured to the private state directory and never
	// logged: vendor diagnostics may echo request content.
	stderrFile, err := os.OpenFile(filepath.Join(stateDir, "runtime.stderr"), os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o600)
	if err != nil {
		return nil, ports.NewRunError(ports.ErrorKindUnavailable, "DeepSeek session state directory is not writable", true, err)
	}
	proc := &sessionProcess{
		sessionID: sessionID, workspaceRoot: workspaceRoot, keyFingerprint: fingerprint,
		ownerUserID: ownerUserID, projectID: projectID,
		done: make(chan struct{}),
	}
	command := exec.Command(m.config.RuntimePath, m.config.runtimeArgs...)
	command.Dir = workspace
	command.Env = m.sessionEnvironment(stateDir, workspace, secret, ownerUserID, projectID)
	command.Stderr = stderrFile
	command.SysProcAttr = &syscall.SysProcAttr{Setpgid: true, Pdeathsig: syscall.SIGKILL}
	command.WaitDelay = shutdownTimeout
	stdin, err := command.StdinPipe()
	if err != nil {
		_ = stderrFile.Close()
		return nil, ports.NewRunError(ports.ErrorKindUnavailable, "DeepSeek session runtime could not be started", true, err)
	}
	stdout, err := command.StdoutPipe()
	if err != nil {
		_ = stderrFile.Close()
		return nil, ports.NewRunError(ports.ErrorKindUnavailable, "DeepSeek session runtime could not be started", true, err)
	}
	if err := command.Start(); err != nil {
		_ = stderrFile.Close()
		return nil, ports.NewRunError(ports.ErrorKindUnavailable, "DeepSeek session runtime could not be started", true, err)
	}
	_ = stderrFile.Close()
	proc.cmd, proc.stdin, proc.reader = command, stdin, newFrameReader(stdout)
	if err := m.initialize(ctx, proc, workspace); err != nil {
		m.terminate(proc)
		return nil, err
	}
	return proc, nil
}

// initialize performs the one-time handshake with the same validation as the
// single-shot executor. Notifications that race the handshake are buffered on
// the process and replayed to the first prompt reader.
func (m *SessionManager) initialize(ctx context.Context, proc *sessionProcess, workspace string) error {
	ctx, cancel := context.WithTimeout(ctx, m.config.Timeout)
	defer cancel()
	stop := m.watchdog(proc, ctx)
	defer stop()

	proc.mu.Lock()
	defer proc.mu.Unlock()
	requestID := proc.nextID + 1
	proc.nextID = requestID
	if err := writeRequest(ctx, proc.stdin, requestID, "initialize", map[string]any{
		"cwd": "/workspace", "provider": "deepseek-official", "model": m.config.Model, "maxTokens": sessionEnvMaxTokens,
	}); err != nil {
		return processError(ctx, err)
	}
	for {
		envelope, err := m.nextFrame(ctx, proc)
		if err != nil {
			return processError(ctx, err)
		}
		if len(envelope.ID) != 0 {
			id, idErr := responseID(envelope)
			if idErr != nil || id != requestID {
				return protocolError("DeepSeek Harness returned an unexpected response", idErr)
			}
			if envelope.Error != nil {
				return classifyRPCError(envelope.Error)
			}
			var initialized initializeResult
			if err := json.Unmarshal(envelope.Result, &initialized); err != nil || initialized.ServerInfo.Name != "workos-deepseek-session" || initialized.ServerInfo.Version == "" {
				return protocolError("DeepSeek Harness initialization response is incompatible", err)
			}
			return nil
		}
		if err := m.bufferNotification(proc, envelope); err != nil {
			return err
		}
	}
}

// sessionEnvironment builds the allowlisted child environment for one
// continuous session. The credential secret enters the child as the runtime's
// API key variable and nowhere else; it never touches harness-host logs or
// configuration. The WORKOS_TOOL_* facts are the read-only WorkOS tool
// context (B04): the server-derived owner/project of this session plus the
// Core listener and the harness device identity for the Connect calls. They
// are authorization facts for the tools, never prompt content: the model can
// read what the tools return but cannot submit owner/project ids to widen
// any scope. Missing facts (no CoreURL/DeviceID configured) are omitted and
// the tools fail closed inside the child.
func (m *SessionManager) sessionEnvironment(stateDir, workspace string, secret []byte, ownerUserID, projectID string) []string {
	home := sessionHomeDir(stateDir)
	environment := []string{
		"HOME=" + home,
		"TMPDIR=" + home,
		"LANG=C.UTF-8",
		"DEEPSEEK_API_KEY=" + string(secret),
		"DEEPSEEK_BASE_URL=" + m.config.BaseURL,
		"DSH_CORDIS_CONFIG=" + filepath.Join(stateDir, "cordis.yml"),
		"DSH_CWD=" + workspace,
		"DSH_MODEL=" + m.config.Model,
		"DSH_MAX_TOKENS=" + strconv.FormatInt(sessionEnvMaxTokens, 10),
		"DSH_HOME=" + home,
	}
	if m.config.CoreURL != "" && m.config.DeviceID != "" && ownerUserID != "" && projectID != "" {
		environment = append(environment,
			"WORKOS_TOOL_CORE_URL="+m.config.CoreURL,
			"WORKOS_TOOL_DEVICE_ID="+m.config.DeviceID,
			"WORKOS_TOOL_OWNER_ID="+ownerUserID,
			"WORKOS_TOOL_PROJECT_ID="+projectID,
		)
	}
	for _, key := range []string{"PATH", "TZ", "SSL_CERT_FILE", "SSL_CERT_DIR"} {
		if value, ok := os.LookupEnv(key); ok {
			environment = append(environment, key+"="+value)
		}
	}
	return append(environment, m.config.runtimeEnv...)
}

// nextFrame drains buffered handshake notifications before reading fresh
// wire frames. Callers must hold proc.mu.
func (m *SessionManager) nextFrame(ctx context.Context, proc *sessionProcess) (rpcEnvelope, error) {
	if len(proc.pending) != 0 {
		next := proc.pending[0]
		proc.pending = proc.pending[1:]
		return next, nil
	}
	return proc.reader.next(ctx)
}

func (m *SessionManager) bufferNotification(proc *sessionProcess, envelope rpcEnvelope) error {
	switch envelope.Method {
	case "session.event", "session.status":
		if len(proc.pending) >= maximumPendingNotifications {
			return protocolError("DeepSeek Harness flooded the session channel", nil)
		}
		proc.pending = append(proc.pending, envelope)
		return nil
	default:
		return protocolError("DeepSeek Harness emitted an unexpected notification", nil)
	}
}

// watchdog kills the child the moment ctx ends before stop is called: the
// frame reader is uninterruptible while blocked reading, so only the
// process-group kill (and the pipe EOF it causes) makes a turn deadline
// observable.
func (m *SessionManager) watchdog(proc *sessionProcess, ctx context.Context) func() {
	stopped := make(chan struct{})
	go func() {
		select {
		case <-ctx.Done():
			m.terminate(proc)
		case <-stopped:
		}
	}()
	return sync.OnceFunc(func() { close(stopped) })
}

// fail terminates the process and drops it from the map: after any turn
// error the stream position is untrusted, so the next Ensure respawns.
func (m *SessionManager) fail(proc *sessionProcess, err error) error {
	m.terminate(proc)
	m.forget(proc)
	return err
}

func (m *SessionManager) forget(proc *sessionProcess) {
	m.mu.Lock()
	if current, ok := m.processes[proc.sessionID]; ok && current == proc {
		delete(m.processes, proc.sessionID)
	}
	m.mu.Unlock()
}

func (m *SessionManager) terminate(proc *sessionProcess) {
	proc.terminateOnce.Do(func() {
		_ = killProcessGroup(proc.cmd, syscall.SIGKILL)
		_ = proc.cmd.Wait()
		close(proc.done)
	})
}

func (m *SessionManager) closeProcess(proc *sessionProcess) {
	proc.mu.Lock()
	defer proc.mu.Unlock()
	ctx, cancel := context.WithTimeout(context.Background(), sessionShutdownWait)
	defer cancel()
	stop := m.watchdog(proc, ctx)
	defer stop()
	proc.nextID++
	if err := writeRequest(ctx, proc.stdin, proc.nextID, "shutdown", nil); err == nil {
		for {
			envelope, readErr := proc.reader.next(ctx)
			if readErr != nil {
				break
			}
			if len(envelope.ID) != 0 {
				break
			}
		}
	}
	_ = proc.stdin.Close()
	m.terminate(proc)
}

func sessionHomeDir(stateDir string) string      { return filepath.Join(stateDir, "home") }
func sessionStateDir(stateDir string) string     { return filepath.Join(stateDir, "state") }
func sessionWorkspaceDir(stateDir string) string { return filepath.Join(stateDir, "ws") }

func sessionPersistenceDir(stateDir string) string { return filepath.Join(stateDir, "persistence") }

// processAlive reports whether the session child has not been terminated.
func processAlive(proc *sessionProcess) bool {
	select {
	case <-proc.done:
		return false
	default:
		return true
	}
}

func credentialFingerprint(secret []byte) string {
	digest := sha256.Sum256(secret)
	return hex.EncodeToString(digest[:])
}

// safeSessionComponent accepts only a plain single path component so the
// Core session id can never traverse the harness state directory.
func safeSessionComponent(value string) bool {
	if value == "" || value == "." || value == ".." || len(value) > 128 {
		return false
	}
	if filepath.Base(value) != value || strings.ContainsAny(value, `/\`) {
		return false
	}
	for _, char := range value {
		if (char < 'a' || char > 'z') && (char < 'A' || char > 'Z') && (char < '0' || char > '9') && char != '-' && char != '_' {
			return false
		}
	}
	return true
}

func ensureSessionDirectory(path string) error {
	if err := os.MkdirAll(path, 0o700); err != nil {
		return ports.NewRunError(ports.ErrorKindUnavailable, "DeepSeek session state directory is unavailable", true, err)
	}
	if err := os.Chmod(path, 0o700); err != nil {
		return ports.NewRunError(ports.ErrorKindUnavailable, "DeepSeek session state directory is unavailable", true, err)
	}
	return nil
}

// cordisRowIDs is the exact official base composition verified loadable by
// the pinned runtime (tmp/b00/probe_cordis.yml) plus the one
// configuration-relative WorkOS row (B04): tool traffic, workspace
// sandboxing, native session persistence, and the read-only WorkOS tools.
var cordisRowIDs = []string{
	"timer", "llm", "session", "session-title", "agent", "jobs", "llm-retry",
	"session-persistence-jsonl", "subprocess", "sandbox", "sandbox-policy",
	"workos-workspace", "shell-env", "approval", "tool-bash", "fs-observation-policy",
	"tool-fs", "agent-instructions",
	"timeout-policy", "tools", "system-prompt", "agent-loop", "llm-deepseek",
	"user-questions", "tool-ask-user", "workos-session", "workos-tools",
}

// workosToolsFileName is the configuration-relative plugin row name. The
// pinned runtime resolves relative row names against the cordis.yml
// directory; the file itself is copied into the session state directory by
// spawn from the adapter's configured WorkosToolsPath.
const workosToolsFileName = "workos-tools.mjs"

// renderCordisConfig writes the official 26-row base row list with exactly
// three sanctioned substitutions: the persistence root, the sandbox
// workspace root, and the llm-deepseek endpoint/model facts. Every other row
// stays byte-identical in name and id; rows that the runtime cannot load
// (permission presets, bash sandbox, compaction/spill/pruner, token meter,
// session checkpoint) must never appear here.
func renderCordisConfig(stateDir, workspace string, config Config) []byte {
	var out strings.Builder
	row := func(id, name string) {
		fmt.Fprintf(&out, "- id: %s\n  name: '%s'\n", id, name)
	}
	row("timer", "@deepseek-ai/cordis-plugin-timer")
	row("llm", "@deepseek-ai/dsh-llm")
	row("session", "@deepseek-ai/dsh-session")
	row("session-title", "@deepseek-ai/dsh-session-title")
	fmt.Fprint(&out, "  config:\n    fallbackMaxWords: 5\n    fallbackMaxBytes: 40\n    maxTitleBytes: 80\n")
	row("agent", "@deepseek-ai/dsh-agent")
	row("jobs", "@deepseek-ai/dsh-jobs-local")
	row("llm-retry", "@deepseek-ai/dsh-llm-retry")
	row("session-persistence-jsonl", "@deepseek-ai/dsh-session-persistence-jsonl")
	fmt.Fprintf(&out, "  config:\n    root: %s\n    compression: none\n", sessionPersistenceDir(stateDir))
	row("subprocess", "@deepseek-ai/dsh-subprocess-local")
	row("sandbox", "@deepseek-ai/dsh-sandbox-local")
	row("sandbox-policy", "@deepseek-ai/dsh-sandbox-policy")
	fmt.Fprintf(&out, "  config:\n    mode: workspace-write\n    workspaceRoot: /workspace\n")
	row("workos-workspace", "./workos-workspace.mjs")
	row("shell-env", "@deepseek-ai/dsh-shell-env")
	row("approval", "@deepseek-ai/dsh-user-approval")
	fmt.Fprint(&out, "  config:\n    policy: ask\n")
	row("tool-bash", "@deepseek-ai/dsh-tool-bash")
	fmt.Fprint(&out, "  config:\n    enableRunInBackground: false\n")
	row("fs-observation-policy", "@deepseek-ai/dsh-fs-observation-policy")

	row("tool-fs", "@deepseek-ai/dsh-tool-fs")

	row("agent-instructions", "@deepseek-ai/dsh-agent-instructions")
	fmt.Fprint(&out, "  config:\n    maxBytes: 65536\n")
	row("timeout-policy", "@deepseek-ai/dsh-tool-call-timeout-policy")
	row("tools", "@deepseek-ai/dsh-tools")
	row("system-prompt", "@deepseek-ai/dsh-system-prompt")
	fmt.Fprint(&out, "  config:\n    persona: 'You are a coding agent.'\n")
	row("agent-loop", "@deepseek-ai/dsh-agent-loop")
	fmt.Fprint(&out, "  config:\n    agents: []\n")
	row("llm-deepseek", "@deepseek-ai/dsh-llm-deepseek")
	fmt.Fprintf(&out, "  config:\n    apiKeyEnv: DEEPSEEK_API_KEY\n    baseURL: %s\n    streamIdleTimeoutMs: 120000\n    retryPolicy:\n      mode: normal\n      maxRetries: 0\n    models:\n      - id: %s\n        contextWindow: 1000000\n        maxTokens: 384000\n", config.BaseURL, config.Model)
	row("user-questions", "@deepseek-ai/dsh-user-questions")
	row("tool-ask-user", "@deepseek-ai/dsh-tool-ask-user")
	row("workos-session", "./workos-session.mjs")
	// The read-only WorkOS tools (B04): configuration-relative row, so the
	// name is the file beside this cordis.yml, never a bare closure package.
	row("workos-tools", "./"+workosToolsFileName)
	return []byte(out.String())
}

func (m *SessionManager) handleTool(ctx context.Context, proc *sessionProcess, envelope rpcEnvelope) error {
	var id int64
	if err := json.Unmarshal(envelope.ID, &id); err != nil || id >= 0 {
		return protocolError("Invalid tool identity", err)
	}
	var request struct {
		Operation string         `json:"operation"`
		Arguments map[string]any `json:"arguments"`
	}
	if err := json.Unmarshal(envelope.Params, &request); err != nil {
		return protocolError("Invalid tool request", err)
	}
	response := map[string]any{"jsonrpc": "2.0", "id": id}
	if proc.tools == nil {
		response["error"] = map[string]any{"code": -32000, "message": "Workspace tools unavailable"}
	} else {
		result, err := proc.tools(ctx, request.Operation, request.Arguments)
		if err != nil {
			response["error"] = map[string]any{"code": -32000, "message": "WorkOS operation failed"}
		} else {
			response["result"] = result
		}
	}
	data, err := json.Marshal(response)
	if err != nil {
		return err
	}
	if len(data) > 1024*1024 {
		return protocolError("Tool response exceeds limit", nil)
	}
	_, err = proc.stdin.Write(append(data, '\n'))
	return err
}
