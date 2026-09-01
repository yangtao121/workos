package config

import (
	"bytes"
	"errors"
	"fmt"
	"net"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/google/uuid"
	"gopkg.in/yaml.v3"
)

// Config is shared deployment configuration. Each process reads only its owned section.
type Config struct {
	Environment string      `yaml:"environment"`
	DatabaseURL string      `yaml:"database_url"`
	HTTP        HTTP        `yaml:"http"`
	Services    URLs        `yaml:"services"`
	Auth        Auth        `yaml:"auth"`
	Agent       Agent       `yaml:"agent"`
	Credential  Credential  `yaml:"credential"`
	Execution   Execution   `yaml:"execution"`
	Harness     Harness     `yaml:"harness"`
	Surface     Surface     `yaml:"surface"`
	Runtime     Runtime     `yaml:"runtime"`
	Indexer     Indexer     `yaml:"indexer"`
	Reliability Reliability `yaml:"reliability"`
	Telemetry   Telemetry   `yaml:"telemetry"`
}

// Reliability configures the supervisor loop: the poll cadence, the stable
// streak behind resolution, and the per-poll incident budget.
type Reliability struct {
	PollInterval         time.Duration `yaml:"poll_interval"`
	PollTimeout          time.Duration `yaml:"poll_timeout"`
	StablePollsToResolve int64         `yaml:"stable_polls_to_resolve"`
	MaxIncidentsPerPoll  int           `yaml:"max_incidents_per_poll"`
}

// Runtime configures the runtime-host Workload Manager timers and the
// verified rootless engine executable. The manager refuses to start on
// out-of-bounds values, and the capability verdict always comes from the
// engine probe — never from the presence of the binary.
// Indexer holds the indexer-only settings. The admin socket stays empty
// unless the operator configures it; no admin surface exists without it.
type Indexer struct {
	AdminSocketPath string `yaml:"admin_socket_path"`
}

type Runtime struct {
	PodmanBin string `yaml:"podman_bin"`
	// IndexerURL configures the runtime's scoped knowledge search upstream.
	// Empty means not configured: knowledge.search is never negotiated.
	IndexerURL        string        `yaml:"indexer_url"`
	IdleTTL           time.Duration `yaml:"idle_ttl"`
	ReconcileInterval time.Duration `yaml:"reconcile_interval"`
	OperationTimeout  time.Duration `yaml:"operation_timeout"`
	CoreGrace         time.Duration `yaml:"core_grace"`
	LeaseTTL          time.Duration `yaml:"lease_ttl"`
	InstanceName      string        `yaml:"instance_name"`
	DeviceID          string        `yaml:"device_id"`
}

// Surface configures the runtime-host Surface Broker session lifetime.
type Surface struct {
	SessionTTL time.Duration `yaml:"session_ttl"`
}

// Telemetry configures the optional OTLP/HTTP trace exporter. An empty endpoint
// keeps the SDK disabled, so local development has no hidden dependency.
type Telemetry struct {
	OTLPEndpoint string `yaml:"otlp_endpoint"`
}

type HTTP struct {
	Address     string `yaml:"address"`
	StaticDir   string `yaml:"static_dir"`
	TLSCertFile string `yaml:"tls_cert_file"`
	TLSKeyFile  string `yaml:"tls_key_file"`
}

type URLs struct {
	Core        string `yaml:"core"`
	Harness     string `yaml:"harness"`
	Runtime     string `yaml:"runtime"`
	Reliability string `yaml:"reliability"`
	Indexer     string `yaml:"indexer"`
}

type Auth struct {
	DevBypass bool   `yaml:"dev_bypass"`
	OwnerID   string `yaml:"owner_id"`
	// DeviceID is the development-mode fixed device identity only.
	// Production device identities are minted by the Gateway device auth
	// service; this value is never used when DevBypass is false.
	DeviceID string `yaml:"device_id"`
	// PublicOrigin is the canonical https origin users and devices trust
	// (production mode): scheme https, no userinfo, no path/query/fragment.
	PublicOrigin string `yaml:"public_origin"`
	// AdminSocketPath is the Gateway-owned admin Unix socket used by
	// workosctl device pair. Production requires it; dev compose leaves it
	// empty (dev bypass needs no operator pairing).
	AdminSocketPath string        `yaml:"admin_socket_path"`
	TicketTTL       time.Duration `yaml:"pairing_ticket_ttl"`
	ChallengeTTL    time.Duration `yaml:"proof_challenge_ttl"`
	SessionTTL      time.Duration `yaml:"session_ttl"`
}

type Agent struct {
	DefaultProvider string               `yaml:"default_provider"`
	CatalogTimeout  time.Duration        `yaml:"catalog_timeout"`
	ProjectBinding  HarnessBindingPreset `yaml:"project_binding"`
}

type HarnessBindingPreset struct {
	InstancePolicy   string `yaml:"instance_policy"`
	ProfileID        string `yaml:"profile_id"`
	ResourcePolicyID string `yaml:"resource_policy_id"`
}

// Credential configures the Core Credential Vault (ADR-0009). Both fields
// are required together; when both are empty the vault is unavailable and
// credential-bearing providers fail closed while every other Core function
// starts normally. The master key file is read only by the vault's crypto
// adapter and never enters configuration dumps or logs.
type Credential struct {
	MasterKeyFile   string `yaml:"master_key_file"`
	AdminSocketPath string `yaml:"admin_socket_path"`
}

// Execution configures the Core's private mutually authenticated TLS harness
// execution listener: the only place TaskExecution and CredentialLease RPCs
// are served. Core and harness-host hold distinct leaf identities issued by
// one explicit private CA; the CA private key never reaches either process.
type Execution struct {
	Address  string `yaml:"address"`
	CAFile   string `yaml:"ca_file"`
	CertFile string `yaml:"cert_file"`
	KeyFile  string `yaml:"key_file"`
}

type Harness struct {
	WorkerID     string        `yaml:"worker_id"`
	PollInterval time.Duration `yaml:"poll_interval"`
	CoreURL      string        `yaml:"core_url"`
	Generic      GenericCLI    `yaml:"generic_cli"`
	DeepSeek     DeepSeek      `yaml:"deepseek"`
	// Execution identity for the private harness execution channel
	// (client side). Required for harness-host to claim tasks at all.
	ExecutionURL      string `yaml:"execution_url"`
	ExecutionCAFile   string `yaml:"execution_ca_file"`
	ExecutionCertFile string `yaml:"execution_cert_file"`
	ExecutionKeyFile  string `yaml:"execution_key_file"`
}

type GenericCLI struct {
	Enabled    bool          `yaml:"enabled"`
	Executable string        `yaml:"executable"`
	Args       []string      `yaml:"args"`
	Timeout    time.Duration `yaml:"timeout"`
}

type DeepSeek struct {
	Enabled          bool          `yaml:"enabled"`
	BaseURL          string        `yaml:"base_url"`
	Model            string        `yaml:"model"`
	Timeout          time.Duration `yaml:"timeout"`
	RuntimePath      string        `yaml:"runtime_path"`
	CordisConfigPath string        `yaml:"cordis_config_path"`
	APIKey           string        `yaml:"-"`

	// ConfigurationIssue lets harness-host start safely and expose an
	// unavailable provider when an adapter-specific environment value is bad.
	ConfigurationIssue string `yaml:"-"`
}

func defaults() Config {
	return Config{
		Environment: "development",
		DatabaseURL: "postgres://workos:workos@127.0.0.1:5432/workos?sslmode=disable",
		HTTP:        HTTP{Address: "127.0.0.1:8080", StaticDir: "apps/desktop-web/dist"},
		Services: URLs{
			Core: "http://127.0.0.1:8081", Harness: "http://127.0.0.1:8082",
			Runtime: "http://127.0.0.1:8083", Reliability: "http://127.0.0.1:8084", Indexer: "http://127.0.0.1:8085",
		},
		Auth: Auth{
			OwnerID:  "0198d7ea-2110-7c42-b659-c5e4d73bc337",
			DeviceID: "0198d7ea-2110-7c42-b659-c5e4d73bc338",
		},
		Agent: Agent{
			DefaultProvider: "fake", CatalogTimeout: 2 * time.Second,
			ProjectBinding: HarnessBindingPreset{InstancePolicy: "ephemeral", ResourcePolicyID: "project-no-tools"},
		},
		Harness: Harness{
			WorkerID: "harness-host-local", PollInterval: 250 * time.Millisecond,
			CoreURL: "http://127.0.0.1:8081",
			Generic: GenericCLI{Timeout: 2 * time.Minute},
			DeepSeek: DeepSeek{
				BaseURL: "https://api.deepseek.com", Model: "deepseek-v4-flash", Timeout: 2 * time.Minute,
				RuntimePath: "/usr/local/libexec/workos/dsh-jsonrpc-agent", CordisConfigPath: "/etc/workos/deepseek.cordis.yml",
			},
		},
		Surface: Surface{SessionTTL: 15 * time.Minute},
		Runtime: Runtime{
			PodmanBin:         "podman",
			IdleTTL:           5 * time.Minute,
			ReconcileInterval: 15 * time.Second,
			OperationTimeout:  2 * time.Minute,
			CoreGrace:         2 * time.Minute,
			LeaseTTL:          30 * time.Second,
			InstanceName:      "runtime-host-local",
			DeviceID:          "0198d7ea-2110-7c42-b659-c5e4d73bc339",
		},
		Reliability: Reliability{
			PollInterval:         5 * time.Second,
			PollTimeout:          4 * time.Second,
			StablePollsToResolve: 3,
			MaxIncidentsPerPoll:  16,
		},
	}
}

// Load reads optional YAML and applies explicit environment overrides.
func Load() (Config, error) {
	cfg := defaults()
	if path := os.Getenv("WORKOS_CONFIG_FILE"); path != "" {
		data, err := os.ReadFile(path)
		if err != nil {
			return Config{}, fmt.Errorf("read config: %w", err)
		}
		decoder := yaml.NewDecoder(bytes.NewReader(data))
		decoder.KnownFields(true)
		if err := decoder.Decode(&cfg); err != nil {
			return Config{}, fmt.Errorf("parse config: %w", err)
		}
	}
	setString(&cfg.DatabaseURL, "WORKOS_DATABASE_URL")
	setString(&cfg.HTTP.Address, "WORKOS_HTTP_ADDRESS")
	setString(&cfg.HTTP.StaticDir, "WORKOS_STATIC_DIR")
	setString(&cfg.HTTP.TLSCertFile, "WORKOS_HTTP_TLS_CERT_FILE")
	setString(&cfg.HTTP.TLSKeyFile, "WORKOS_HTTP_TLS_KEY_FILE")
	setString(&cfg.Services.Core, "WORKOS_CORE_URL")
	setString(&cfg.Services.Harness, "WORKOS_HARNESS_URL")
	setString(&cfg.Services.Runtime, "WORKOS_RUNTIME_URL")
	setString(&cfg.Services.Indexer, "WORKOS_INDEXER_URL")
	setString(&cfg.Harness.CoreURL, "WORKOS_CORE_URL")
	setString(&cfg.Auth.OwnerID, "WORKOS_OWNER_ID")
	setString(&cfg.Auth.DeviceID, "WORKOS_DEVICE_ID")
	setString(&cfg.Auth.PublicOrigin, "WORKOS_AUTH_PUBLIC_ORIGIN")
	setString(&cfg.Auth.AdminSocketPath, "WORKOS_AUTH_ADMIN_SOCKET")
	setString(&cfg.Credential.MasterKeyFile, "WORKOS_CREDENTIAL_MASTER_KEY_FILE")
	setString(&cfg.Credential.AdminSocketPath, "WORKOS_CREDENTIAL_ADMIN_SOCKET")
	setString(&cfg.Execution.Address, "WORKOS_CORE_EXECUTION_ADDRESS")
	setString(&cfg.Execution.CAFile, "WORKOS_CORE_EXECUTION_CA_FILE")
	setString(&cfg.Execution.CertFile, "WORKOS_CORE_EXECUTION_CERT_FILE")
	setString(&cfg.Execution.KeyFile, "WORKOS_CORE_EXECUTION_KEY_FILE")
	setString(&cfg.Harness.ExecutionURL, "WORKOS_CORE_EXECUTION_URL")
	setString(&cfg.Harness.ExecutionCAFile, "WORKOS_HARNESS_EXECUTION_CA_FILE")
	setString(&cfg.Harness.ExecutionCertFile, "WORKOS_HARNESS_EXECUTION_CERT_FILE")
	setString(&cfg.Harness.ExecutionKeyFile, "WORKOS_HARNESS_EXECUTION_KEY_FILE")
	for _, bound := range []struct {
		key string
		dst *time.Duration
	}{
		{"WORKOS_AUTH_PAIRING_TICKET_TTL", &cfg.Auth.TicketTTL},
		{"WORKOS_AUTH_PROOF_CHALLENGE_TTL", &cfg.Auth.ChallengeTTL},
		{"WORKOS_AUTH_SESSION_TTL", &cfg.Auth.SessionTTL},
	} {
		if raw, ok := os.LookupEnv(bound.key); ok {
			value, err := time.ParseDuration(raw)
			if err != nil || value <= 0 {
				return Config{}, fmt.Errorf("%s must be a positive duration", bound.key)
			}
			*bound.dst = value
		}
	}
	setString(&cfg.Agent.DefaultProvider, "WORKOS_AGENT_DEFAULT_PROVIDER")
	setString(&cfg.Agent.ProjectBinding.InstancePolicy, "WORKOS_PROJECT_HARNESS_INSTANCE_POLICY")
	setString(&cfg.Agent.ProjectBinding.ProfileID, "WORKOS_PROJECT_HARNESS_PROFILE_ID")
	setString(&cfg.Agent.ProjectBinding.ResourcePolicyID, "WORKOS_PROJECT_HARNESS_RESOURCE_POLICY_ID")
	// DEEPSEEK_API_KEY is retired (ADR-0009): a set value is reported as a
	// configuration issue directing the operator to the vault. The value is
	// never read, never echoed, and never used — the harness-host DeepSeek
	// provider will report unavailable until the credential moves to the
	// Credential Vault.
	if legacyKey, legacySet := os.LookupEnv("DEEPSEEK_API_KEY"); legacySet && strings.TrimSpace(legacyKey) != "" {
		cfg.Harness.DeepSeek.ConfigurationIssue = "DEEPSEEK_API_KEY is retired: store the provider credential in the WorkOS Credential Vault with workosctl credential put"
	}
	setString(&cfg.Harness.DeepSeek.BaseURL, "WORKOS_DEEPSEEK_BASE_URL")
	setString(&cfg.Harness.DeepSeek.Model, "WORKOS_DEEPSEEK_MODEL")
	setString(&cfg.Harness.DeepSeek.RuntimePath, "WORKOS_DEEPSEEK_RUNTIME_PATH")
	setString(&cfg.Harness.DeepSeek.CordisConfigPath, "WORKOS_DEEPSEEK_CORDIS_CONFIG")
	setString(&cfg.Telemetry.OTLPEndpoint, "OTEL_EXPORTER_OTLP_ENDPOINT")
	if raw, ok := os.LookupEnv("WORKOS_DEEPSEEK_ENABLED"); ok {
		value, err := strconv.ParseBool(raw)
		if err != nil {
			cfg.Harness.DeepSeek.ConfigurationIssue = "WORKOS_DEEPSEEK_ENABLED is invalid"
		} else {
			cfg.Harness.DeepSeek.Enabled = value
		}
	}
	if raw, ok := os.LookupEnv("WORKOS_DEEPSEEK_TIMEOUT"); ok {
		value, err := time.ParseDuration(raw)
		if err != nil {
			cfg.Harness.DeepSeek.ConfigurationIssue = "WORKOS_DEEPSEEK_TIMEOUT is invalid"
		} else {
			cfg.Harness.DeepSeek.Timeout = value
		}
	}
	if raw, ok := os.LookupEnv("WORKOS_AGENT_CATALOG_TIMEOUT"); ok {
		value, err := time.ParseDuration(raw)
		if err != nil || value <= 0 {
			return Config{}, errors.New("WORKOS_AGENT_CATALOG_TIMEOUT must be a positive duration")
		}
		cfg.Agent.CatalogTimeout = value
	}
	setString(&cfg.Runtime.PodmanBin, "WORKOS_RUNTIME_PODMAN_BIN")
	setString(&cfg.Runtime.IndexerURL, "WORKOS_RUNTIME_INDEXER_URL")
	setString(&cfg.Indexer.AdminSocketPath, "WORKOS_INDEX_ADMIN_SOCKET")
	setString(&cfg.Runtime.InstanceName, "WORKOS_RUNTIME_INSTANCE_NAME")
	setString(&cfg.Runtime.DeviceID, "WORKOS_RUNTIME_DEVICE_ID")
	for _, override := range []struct {
		key   string
		dst   *time.Duration
		lower string
	}{
		{"WORKOS_RUNTIME_IDLE_TTL", &cfg.Runtime.IdleTTL, "WORKOS_RUNTIME_IDLE_TTL"},
		{"WORKOS_RUNTIME_RECONCILE_INTERVAL", &cfg.Runtime.ReconcileInterval, "WORKOS_RUNTIME_RECONCILE_INTERVAL"},
		{"WORKOS_RUNTIME_OPERATION_TIMEOUT", &cfg.Runtime.OperationTimeout, "WORKOS_RUNTIME_OPERATION_TIMEOUT"},
		{"WORKOS_RUNTIME_CORE_GRACE", &cfg.Runtime.CoreGrace, "WORKOS_RUNTIME_CORE_GRACE"},
		{"WORKOS_RUNTIME_LEASE_TTL", &cfg.Runtime.LeaseTTL, "WORKOS_RUNTIME_LEASE_TTL"},
	} {
		if raw, ok := os.LookupEnv(override.key); ok {
			value, err := time.ParseDuration(raw)
			if err != nil || value <= 0 {
				return Config{}, fmt.Errorf("%s must be a positive duration", override.lower)
			}
			*override.dst = value
		}
	}
	if raw, ok := os.LookupEnv("WORKOS_RELIABILITY_POLL_INTERVAL"); ok {
		value, err := time.ParseDuration(raw)
		if err != nil || value <= 0 {
			return Config{}, errors.New("WORKOS_RELIABILITY_POLL_INTERVAL must be a positive duration")
		}
		cfg.Reliability.PollInterval = value
	}
	if raw, ok := os.LookupEnv("WORKOS_SURFACE_SESSION_TTL"); ok {
		value, err := time.ParseDuration(raw)
		if err != nil || value <= 0 {
			return Config{}, errors.New("WORKOS_SURFACE_SESSION_TTL must be a positive duration")
		}
		cfg.Surface.SessionTTL = value
	}
	if raw, ok := os.LookupEnv("WORKOS_DEV_AUTH_BYPASS"); ok {
		value, err := strconv.ParseBool(raw)
		if err != nil {
			return Config{}, fmt.Errorf("parse WORKOS_DEV_AUTH_BYPASS: %w", err)
		}
		cfg.Auth.DevBypass = value
	}
	return cfg, nil
}

func setString(dst *string, key string) {
	if value, ok := os.LookupEnv(key); ok {
		*dst = value
	}
}

// validUpstreamURL accepts exactly the shapes a reverse-proxy upstream can
// have: an absolute http/https URL with a non-empty host. Relative paths,
// scheme-less strings, unsupported schemes, and empty values are rejected at
// startup so a bad upstream can never fail lazily on first request.
func validUpstreamURL(raw string) bool {
	parsed, err := url.Parse(raw)
	if err != nil || !parsed.IsAbs() || parsed.Host == "" {
		return false
	}
	return parsed.Scheme == "http" || parsed.Scheme == "https"
}

// ValidateGateway enforces fail-closed public binding rules.
func (c Config) ValidateGateway() error {
	host, _, err := net.SplitHostPort(c.HTTP.Address)
	if err != nil {
		return fmt.Errorf("invalid HTTP address: %w", err)
	}
	if c.Auth.DevBypass {
		if !isLoopback(host) {
			return errors.New("development auth bypass requires a loopback bind address")
		}
		if c.Auth.OwnerID == "" || c.Auth.DeviceID == "" {
			return errors.New("owner and device identity are required")
		}
	} else if err := c.validateGatewayProduction(); err != nil {
		return err
	}
	if !isLoopback(host) && (c.HTTP.TLSCertFile == "" || c.HTTP.TLSKeyFile == "") {
		return errors.New("non-loopback gateway requires TLS certificate and key")
	}
	if !validUpstreamURL(c.Services.Core) {
		return errors.New("invalid core URL: must be an absolute http(s) URL with a host")
	}
	if !validUpstreamURL(c.Services.Runtime) {
		return errors.New("invalid runtime URL: must be an absolute http(s) URL with a host")
	}
	if strings.TrimSpace(c.Services.Reliability) != "" && !validUpstreamURL(c.Services.Reliability) {
		return errors.New("invalid reliability URL: must be an absolute http(s) URL with a host")
	}
	if strings.TrimSpace(c.Services.Indexer) != "" && !validUpstreamURL(c.Services.Indexer) {
		return errors.New("invalid indexer URL: must be an absolute http(s) URL with a host")
	}
	return nil
}

// Device auth TTL bounds (ADR-0007). Kept as local grammar constants so the
// shared platform config never imports a process-internal package.
const (
	authTicketMinTTL    = time.Minute
	authTicketMaxTTL    = 15 * time.Minute
	authChallengeMinTTL = 30 * time.Second
	authChallengeMaxTTL = 5 * time.Minute
	authSessionMinTTL   = 5 * time.Minute
	authSessionMaxTTL   = 30 * 24 * time.Hour
)

// validateGatewayProduction enforces the production device-auth deployment
// grammar: the Gateway terminates its own TLS on a canonical public origin,
// has a canonical UUIDv7 owner, a reachable PostgreSQL URL, and a secure
// admin Unix socket path.
func (c Config) validateGatewayProduction() error {
	if c.HTTP.TLSCertFile == "" || c.HTTP.TLSKeyFile == "" {
		return errors.New("production auth requires the gateway to terminate TLS (certificate and key)")
	}
	origin, err := url.Parse(c.Auth.PublicOrigin)
	if err != nil || origin.Scheme != "https" || origin.Host == "" ||
		origin.User != nil || origin.Path != "" || origin.RawQuery != "" || origin.Fragment != "" ||
		origin.Opaque != "" {
		return errors.New("public origin must be a canonical https origin without userinfo, path, query, or fragment")
	}
	if !canonicalUUIDv7(c.Auth.OwnerID) {
		return errors.New("owner id must be a canonical lowercase UUIDv7 in production")
	}
	lower := strings.ToLower(c.DatabaseURL)
	if !strings.HasPrefix(lower, "postgres://") && !strings.HasPrefix(lower, "postgresql://") {
		return errors.New("production auth requires a postgres database URL")
	}
	if err := ValidateAdminSocketPath(c.Auth.AdminSocketPath); err != nil {
		return err
	}
	if c.Auth.TicketTTL != 0 && (c.Auth.TicketTTL < authTicketMinTTL || c.Auth.TicketTTL > authTicketMaxTTL) {
		return errors.New("pairing ticket TTL must be between 1m and 15m")
	}
	if c.Auth.ChallengeTTL != 0 && (c.Auth.ChallengeTTL < authChallengeMinTTL || c.Auth.ChallengeTTL > authChallengeMaxTTL) {
		return errors.New("proof challenge TTL must be between 30s and 5m")
	}
	if c.Auth.SessionTTL != 0 && (c.Auth.SessionTTL < authSessionMinTTL || c.Auth.SessionTTL > authSessionMaxTTL) {
		return errors.New("device session TTL must be between 5m and 30d")
	}
	return nil
}

// ValidateAdminSocketPath accepts only a plain, absolute path inside a real,
// process-owned directory that group/other users cannot write. The socket
// itself must either not exist or be an owner-matched stale Unix socket.
// Symlinks, uncontrolled runtime directories, regular files, directories,
// and relative paths fail startup instead of being cleaned up.
func ValidateAdminSocketPath(path string) error {
	if path == "" {
		return errors.New("production auth requires the admin Unix socket path")
	}
	if !filepath.IsAbs(path) || filepath.Clean(path) != path {
		return errors.New("admin socket path must be an absolute, cleaned path")
	}
	parent := filepath.Dir(path)
	if !filepath.IsAbs(parent) || filepath.Clean(parent) != parent {
		return errors.New("admin socket parent must be an absolute, cleaned directory")
	}
	info, err := os.Lstat(parent)
	if err != nil {
		return fmt.Errorf("admin socket parent is unavailable: %w", err)
	}
	if !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
		return errors.New("admin socket parent must be a real directory, not a symlink")
	}
	stat, ok := info.Sys().(*syscall.Stat_t)
	if !ok || stat.Uid != uint32(os.Geteuid()) {
		return errors.New("admin socket parent must be owned by the gateway process user")
	}
	if info.Mode().Perm()&0o022 != 0 {
		return errors.New("admin socket parent must not be writable by group or others")
	}
	resolved, err := filepath.EvalSymlinks(parent)
	if err != nil || filepath.Clean(resolved) != parent {
		return errors.New("admin socket parent must not contain symlinks")
	}
	if stat, err := os.Lstat(path); err == nil {
		if stat.Mode()&os.ModeSocket == 0 {
			return errors.New("admin socket path exists and is not a Unix socket")
		}
		owner, ok := stat.Sys().(*syscall.Stat_t)
		if !ok || owner.Uid != uint32(os.Geteuid()) {
			return errors.New("existing admin socket must be owned by the gateway process user")
		}
	} else if !errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("admin socket path is unavailable: %w", err)
	}
	return nil
}

// canonicalUUIDv7 reports whether value is the canonical lowercase UUIDv7
// form.
func canonicalUUIDv7(value string) bool {
	parsed, err := uuid.Parse(value)
	if err != nil {
		return false
	}
	return parsed.String() == value && parsed.Version() == 7
}

// ValidateRuntimeHost checks Runtime-owned routing and surface session
// configuration before the surface broker begins accepting requests.
func (c Config) ValidateRuntimeHost() error {
	coreURL, err := url.Parse(c.Services.Core)
	if err != nil || !coreURL.IsAbs() || coreURL.Host == "" || (coreURL.Scheme != "http" && coreURL.Scheme != "https") {
		return errors.New("invalid Core service URL")
	}
	if c.Surface.SessionTTL < time.Minute || c.Surface.SessionTTL > 24*time.Hour {
		return errors.New("surface session TTL must be between 1m and 24h")
	}
	if strings.TrimSpace(c.Runtime.InstanceName) == "" {
		return errors.New("runtime instance name is required")
	}
	if c.Runtime.DeviceID == "" {
		return errors.New("runtime service device identity is required")
	}
	return nil
}

// ValidateReliabilityHost checks the supervisor loop bounds before the
// reliability host begins observing.
func (c Config) ValidateReliabilityHost() error {
	runtimeURL, err := url.Parse(c.Services.Runtime)
	if err != nil || !runtimeURL.IsAbs() || runtimeURL.Host == "" || (runtimeURL.Scheme != "http" && runtimeURL.Scheme != "https") {
		return errors.New("invalid Runtime service URL")
	}
	if c.Reliability.PollInterval < time.Second || c.Reliability.PollInterval > time.Hour {
		return errors.New("reliability poll interval must be between 1s and 1h")
	}
	if c.Reliability.PollTimeout < 100*time.Millisecond || c.Reliability.PollTimeout >= c.Reliability.PollInterval {
		return errors.New("reliability poll timeout must be positive and below the poll interval")
	}
	if c.Reliability.StablePollsToResolve < 1 || c.Reliability.StablePollsToResolve > 1000 {
		return errors.New("reliability stable poll threshold must be between 1 and 1000")
	}
	if c.Reliability.MaxIncidentsPerPoll < 1 || c.Reliability.MaxIncidentsPerPoll > 1000 {
		return errors.New("reliability per-poll incident budget must be between 1 and 1000")
	}
	return nil
}

// ValidateHarness checks the harness-host's private execution identity
// before the worker starts claiming tasks: the mTLS execution channel is the
// only path to TaskExecution and CredentialLease RPCs.
func (c Config) ValidateHarness() error {
	executionURL, err := url.Parse(c.Harness.ExecutionURL)
	if err != nil || !executionURL.IsAbs() || executionURL.Host == "" || executionURL.Scheme != "https" {
		return errors.New("harness execution URL must be an absolute https URL")
	}
	for _, field := range []string{c.Harness.ExecutionCAFile, c.Harness.ExecutionCertFile, c.Harness.ExecutionKeyFile} {
		if strings.TrimSpace(field) == "" {
			return errors.New("harness execution identity requires CA, certificate, and key files")
		}
	}
	return nil
}

// ValidateCore checks Core-owned routing, Catalog, and binding configuration
// before any public service begins accepting requests.
func (c Config) ValidateCore() error {
	if strings.TrimSpace(c.Agent.DefaultProvider) == "" {
		return errors.New("agent default provider is required")
	}
	// The private harness execution listener is mandatory: plain TaskExecution
	// RPCs no longer exist on the ordinary Core listener.
	if strings.TrimSpace(c.Execution.Address) == "" {
		return errors.New("harness execution listener address is required")
	}
	if _, _, err := net.SplitHostPort(c.Execution.Address); err != nil {
		return fmt.Errorf("invalid harness execution address: %w", err)
	}
	for _, field := range []string{c.Execution.CAFile, c.Execution.CertFile, c.Execution.KeyFile} {
		if strings.TrimSpace(field) == "" {
			return errors.New("harness execution listener requires CA, certificate, and key files")
		}
	}
	// The credential vault is all-or-nothing: partial configuration fails
	// startup instead of half-serving secrets.
	vaultSet := c.Credential.MasterKeyFile != "" || c.Credential.AdminSocketPath != ""
	if vaultSet && (c.Credential.MasterKeyFile == "" || c.Credential.AdminSocketPath == "") {
		return errors.New("credential vault requires both the master key file and the admin socket path")
	}
	if c.Credential.AdminSocketPath != "" {
		if err := ValidateAdminSocketPath(c.Credential.AdminSocketPath); err != nil {
			return err
		}
	}
	harnessURL, err := url.Parse(c.Services.Harness)
	if err != nil || !harnessURL.IsAbs() || harnessURL.Host == "" || (harnessURL.Scheme != "http" && harnessURL.Scheme != "https") {
		return errors.New("invalid Harness service URL")
	}
	if c.Agent.CatalogTimeout < 100*time.Millisecond || c.Agent.CatalogTimeout > 30*time.Second {
		return errors.New("agent Catalog timeout must be between 100ms and 30s")
	}
	if strings.TrimSpace(c.Agent.ProjectBinding.ResourcePolicyID) == "" {
		return errors.New("Project Harness resource policy reference is required")
	}
	switch strings.TrimSpace(c.Agent.ProjectBinding.InstancePolicy) {
	case "persistent", "lazy", "ephemeral":
		return nil
	default:
		return errors.New("invalid Project Harness instance policy")
	}
}

func isLoopback(host string) bool {
	if host == "localhost" {
		return true
	}
	ip := net.ParseIP(host)
	return ip != nil && ip.IsLoopback()
}
