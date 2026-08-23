package config

import (
	"bytes"
	"errors"
	"fmt"
	"net"
	"net/url"
	"os"
	"strconv"
	"time"

	"gopkg.in/yaml.v3"
)

// Config is shared deployment configuration. Each process reads only its owned section.
type Config struct {
	Environment string    `yaml:"environment"`
	DatabaseURL string    `yaml:"database_url"`
	HTTP        HTTP      `yaml:"http"`
	Services    URLs      `yaml:"services"`
	Auth        Auth      `yaml:"auth"`
	Agent       Agent     `yaml:"agent"`
	Harness     Harness   `yaml:"harness"`
	Telemetry   Telemetry `yaml:"telemetry"`
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
	DeviceID  string `yaml:"device_id"`
}

type Agent struct {
	DefaultProvider string `yaml:"default_provider"`
}

type Harness struct {
	WorkerID     string        `yaml:"worker_id"`
	PollInterval time.Duration `yaml:"poll_interval"`
	CoreURL      string        `yaml:"core_url"`
	Generic      GenericCLI    `yaml:"generic_cli"`
	DeepSeek     DeepSeek      `yaml:"deepseek"`
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
		Agent: Agent{DefaultProvider: "fake"},
		Harness: Harness{
			WorkerID: "harness-host-local", PollInterval: 250 * time.Millisecond,
			CoreURL: "http://127.0.0.1:8081",
			Generic: GenericCLI{Timeout: 2 * time.Minute},
			DeepSeek: DeepSeek{
				BaseURL: "https://api.deepseek.com", Model: "deepseek-v4-flash", Timeout: 2 * time.Minute,
				RuntimePath: "/usr/local/libexec/workos/dsh-jsonrpc-agent", CordisConfigPath: "/etc/workos/deepseek.cordis.yml",
			},
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
	setString(&cfg.Services.Core, "WORKOS_CORE_URL")
	setString(&cfg.Harness.CoreURL, "WORKOS_CORE_URL")
	setString(&cfg.Auth.OwnerID, "WORKOS_OWNER_ID")
	setString(&cfg.Auth.DeviceID, "WORKOS_DEVICE_ID")
	setString(&cfg.Agent.DefaultProvider, "WORKOS_AGENT_DEFAULT_PROVIDER")
	setString(&cfg.Harness.DeepSeek.APIKey, "DEEPSEEK_API_KEY")
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

// ValidateGateway enforces fail-closed public binding rules.
func (c Config) ValidateGateway() error {
	host, _, err := net.SplitHostPort(c.HTTP.Address)
	if err != nil {
		return fmt.Errorf("invalid HTTP address: %w", err)
	}
	if c.Auth.DevBypass && !isLoopback(host) {
		return errors.New("development auth bypass requires a loopback bind address")
	}
	if !isLoopback(host) && (c.HTTP.TLSCertFile == "" || c.HTTP.TLSKeyFile == "") {
		return errors.New("non-loopback gateway requires TLS certificate and key")
	}
	if c.Auth.OwnerID == "" || c.Auth.DeviceID == "" {
		return errors.New("owner and device identity are required")
	}
	if _, err := url.ParseRequestURI(c.Services.Core); err != nil {
		return fmt.Errorf("invalid core URL: %w", err)
	}
	return nil
}

func isLoopback(host string) bool {
	if host == "localhost" {
		return true
	}
	ip := net.ParseIP(host)
	return ip != nil && ip.IsLoopback()
}
