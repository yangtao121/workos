package deepseek

import (
	"errors"
	"fmt"
	"net"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"time"
)

const (
	ProviderID          = "deepseek"
	AdapterVersion      = "1.0.0"
	DefaultBaseURL      = "https://api.deepseek.com"
	DefaultModel        = "deepseek-v4-flash"
	DefaultMaxTokens    = int64(8192)
	MaximumMaxTokens    = int64(384000)
	DefaultTimeout      = 2 * time.Minute
	MaximumTimeout      = 10 * time.Minute
	DefaultRuntimePath  = "/usr/local/libexec/workos/dsh-jsonrpc-agent"
	DefaultCordisConfig = "/etc/workos/deepseek.cordis.yml"

	maximumGoalBytes = 4 * 1024 * 1024
)

var supportedModels = map[string]struct{}{
	"deepseek-v4-flash": {},
	"deepseek-v4-pro":   {},
}

// Config contains only process-local adapter configuration. APIKey is populated
// from DEEPSEEK_API_KEY by the platform config loader and must never be serialized.
type Config struct {
	Enabled            bool
	Environment        string
	APIKey             string
	BaseURL            string
	Model              string
	Timeout            time.Duration
	RuntimePath        string
	CordisConfigPath   string
	ConfigurationIssue string

	// runtimeArgs and runtimeEnv exist solely for package tests. Production starts
	// the pinned runtime with no caller-controlled arguments or environment.
	runtimeArgs []string
	runtimeEnv  []string
}

func normalizeConfig(config Config) Config {
	config.Environment = strings.TrimSpace(config.Environment)
	if config.Environment == "" {
		config.Environment = "production"
	}
	config.BaseURL = strings.TrimSpace(config.BaseURL)
	if config.BaseURL == "" {
		config.BaseURL = DefaultBaseURL
	}
	config.BaseURL = strings.TrimRight(config.BaseURL, "/")
	config.Model = strings.TrimSpace(config.Model)
	if config.Model == "" {
		config.Model = DefaultModel
	}
	if config.Timeout == 0 {
		config.Timeout = DefaultTimeout
	}
	config.RuntimePath = strings.TrimSpace(config.RuntimePath)
	if config.RuntimePath == "" {
		config.RuntimePath = DefaultRuntimePath
	}
	config.CordisConfigPath = strings.TrimSpace(config.CordisConfigPath)
	if config.CordisConfigPath == "" {
		config.CordisConfigPath = DefaultCordisConfig
	}
	config.ConfigurationIssue = strings.TrimSpace(config.ConfigurationIssue)
	return config
}

func validateConfig(config Config) error {
	if config.ConfigurationIssue != "" {
		return errors.New("DeepSeek configuration contains an invalid value")
	}
	if !config.Enabled {
		return errors.New("DeepSeek provider is disabled")
	}
	if config.APIKey == "" {
		return errors.New("DeepSeek API key is not configured")
	}
	if len(config.APIKey) > 8*1024 || strings.ContainsAny(config.APIKey, "\r\n") {
		return errors.New("DeepSeek API key is invalid")
	}
	parsed, err := url.Parse(config.BaseURL)
	if err != nil || !parsed.IsAbs() || parsed.Host == "" || parsed.User != nil || parsed.RawQuery != "" || parsed.Fragment != "" {
		return errors.New("DeepSeek base URL is invalid")
	}
	if parsed.Scheme != "https" {
		if parsed.Scheme != "http" || !allowsLoopbackHTTP(config.Environment, parsed.Hostname()) {
			return errors.New("DeepSeek base URL must use HTTPS")
		}
	}
	if _, ok := supportedModels[config.Model]; !ok {
		return fmt.Errorf("DeepSeek model %q is not supported", config.Model)
	}
	if config.Timeout < time.Second || config.Timeout > MaximumTimeout {
		return errors.New("DeepSeek timeout must be between 1s and 10m")
	}
	if err := validateExecutable(config.RuntimePath); err != nil {
		return err
	}
	if err := validateReadableFile(config.CordisConfigPath); err != nil {
		return err
	}
	return nil
}

func allowsLoopbackHTTP(environment, hostname string) bool {
	if environment != "development" && environment != "test" {
		return false
	}
	if strings.EqualFold(hostname, "localhost") {
		return true
	}
	ip := net.ParseIP(hostname)
	return ip != nil && ip.IsLoopback()
}

func validateExecutable(path string) error {
	if !filepath.IsAbs(path) {
		return errors.New("DeepSeek Harness runtime path must be absolute")
	}
	info, err := os.Stat(path)
	if err != nil || !info.Mode().IsRegular() || info.Mode().Perm()&0o111 == 0 {
		return errors.New("DeepSeek Harness runtime is unavailable")
	}
	return nil
}

func validateReadableFile(path string) error {
	if !filepath.IsAbs(path) {
		return errors.New("DeepSeek Harness config path must be absolute")
	}
	info, err := os.Stat(path)
	if err != nil || !info.Mode().IsRegular() || info.Mode().Perm()&0o444 == 0 {
		return errors.New("DeepSeek Harness config is unavailable")
	}
	return nil
}
