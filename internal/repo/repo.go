// Package repo manages the .git/safegit/ data directory.
// It handles initialization, validation, and provides path helpers.
package repo

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
)

// Config holds safegit configuration persisted in config.json.
type Config struct {
	SchemaVersion int          `json:"schemaVersion"`
	Commit        CommitConfig `json:"commit"`
	Lock          LockConfig   `json:"lock"`
	Hooks         HooksConfig  `json:"hooks"`
	Push          PushConfig   `json:"push"`
	Log           LogConfig    `json:"log"`
}

type CommitConfig struct {
	CASMaxAttempts int `json:"casMaxAttempts"`
}

type LockConfig struct {
	AcquireTimeoutSeconds int `json:"acquireTimeoutSeconds"`
}

type HooksConfig struct {
	PrePrePush PrePrePushConfig `json:"preprepush"`
}

type PrePrePushConfig struct {
	TimeoutSeconds int `json:"timeoutSeconds"`
}

type PushConfig struct {
	RetryAttempts int `json:"retryAttempts"`
}

type LogConfig struct {
	MaxSizeMB int `json:"maxSizeMB"`
}

// DefaultConfig returns the default safegit configuration.
func DefaultConfig() Config {
	return Config{
		SchemaVersion: 1,
		Commit:        CommitConfig{CASMaxAttempts: 5},
		Lock:          LockConfig{AcquireTimeoutSeconds: 30},
		Hooks:         HooksConfig{PrePrePush: PrePrePushConfig{TimeoutSeconds: 1800}},
		Push:          PushConfig{RetryAttempts: 3},
		Log:           LogConfig{MaxSizeMB: 100},
	}
}

// SafegitDir returns the path to .git/safegit/ given a .git directory path.
func SafegitDir(gitDir string) string {
	return filepath.Join(gitDir, "safegit")
}

// IsInitialized checks whether .git/safegit/ exists and has config.json.
func IsInitialized(gitDir string) bool {
	configPath := filepath.Join(SafegitDir(gitDir), "config.json")
	_, err := os.Stat(configPath)
	return err == nil
}

// Init creates the .git/safegit/ directory structure and writes default config.json.
// Returns an error if already initialized (use --force to reinitialize).
func Init(gitDir string, force bool) error {
	sgDir := SafegitDir(gitDir)

	if IsInitialized(gitDir) && !force {
		return fmt.Errorf("safegit already initialized at %s (use --force to reinitialize)", sgDir)
	}

	// Create directory structure
	dirs := []string{
		sgDir,
		filepath.Join(sgDir, "locks", "refs", "heads"),
		filepath.Join(sgDir, "queue", "refs", "heads"),
		filepath.Join(sgDir, "wip-locks"),
		filepath.Join(sgDir, "tmp"),
	}
	for _, d := range dirs {
		if err := os.MkdirAll(d, 0755); err != nil {
			return fmt.Errorf("creating directory %s: %w", d, err)
		}
	}

	// Write config.json
	cfg := DefaultConfig()
	data, err := json.MarshalIndent(cfg, "", "  ")
	if err != nil {
		return fmt.Errorf("marshaling config: %w", err)
	}
	configPath := filepath.Join(sgDir, "config.json")
	if err := os.WriteFile(configPath, append(data, '\n'), 0644); err != nil {
		return fmt.Errorf("writing config.json: %w", err)
	}

	// Create empty log file
	logPath := filepath.Join(sgDir, "log")
	f, err := os.OpenFile(logPath, os.O_CREATE|os.O_WRONLY, 0644)
	if err != nil {
		return fmt.Errorf("creating log file: %w", err)
	}
	f.Close()

	return nil
}

// EnsureInitialized returns an error if .git/safegit/ is not set up.
func EnsureInitialized(gitDir string) error {
	if !IsInitialized(gitDir) {
		return errors.New("safegit not initialized; run 'safegit init' first")
	}
	return nil
}

// Uninstall removes the .git/safegit/ directory entirely.
func Uninstall(gitDir string) error {
	sgDir := SafegitDir(gitDir)
	if _, err := os.Stat(sgDir); os.IsNotExist(err) {
		return errors.New("safegit is not initialized (nothing to remove)")
	}
	return os.RemoveAll(sgDir)
}

// LoadConfig reads and parses config.json from the safegit directory.
func LoadConfig(gitDir string) (*Config, error) {
	configPath := filepath.Join(SafegitDir(gitDir), "config.json")
	data, err := os.ReadFile(configPath)
	if err != nil {
		return nil, fmt.Errorf("reading config.json: %w", err)
	}
	var cfg Config
	if err := json.Unmarshal(data, &cfg); err != nil {
		return nil, fmt.Errorf("parsing config.json: %w", err)
	}
	return &cfg, nil
}

// SaveConfig writes config back to config.json.
func SaveConfig(gitDir string, cfg *Config) error {
	configPath := filepath.Join(SafegitDir(gitDir), "config.json")
	data, err := json.MarshalIndent(cfg, "", "  ")
	if err != nil {
		return fmt.Errorf("marshaling config: %w", err)
	}
	if err := os.WriteFile(configPath, append(data, '\n'), 0644); err != nil {
		return fmt.Errorf("writing config.json: %w", err)
	}
	return nil
}

// GetConfigValue returns the value for a dot-separated config key.
func GetConfigValue(cfg *Config, key string) (interface{}, error) {
	switch key {
	case "commit.casMaxAttempts":
		return cfg.Commit.CASMaxAttempts, nil
	case "lock.acquireTimeoutSeconds":
		return cfg.Lock.AcquireTimeoutSeconds, nil
	case "hooks.preprepush.timeoutSeconds":
		return cfg.Hooks.PrePrePush.TimeoutSeconds, nil
	case "push.retryAttempts":
		return cfg.Push.RetryAttempts, nil
	case "log.maxSizeMB":
		return cfg.Log.MaxSizeMB, nil
	default:
		return nil, fmt.Errorf("unknown config key: %s", key)
	}
}

// SetConfigValue sets a dot-separated config key to the given string value.
func SetConfigValue(cfg *Config, key, value string) error {
	intVal, err := parseInt(value)
	if err != nil {
		return fmt.Errorf("invalid value %q for %s: must be an integer", value, key)
	}
	if intVal <= 0 {
		return fmt.Errorf("invalid value %q for %s: must be positive", value, key)
	}

	switch key {
	case "commit.casMaxAttempts":
		cfg.Commit.CASMaxAttempts = intVal
	case "lock.acquireTimeoutSeconds":
		cfg.Lock.AcquireTimeoutSeconds = intVal
	case "hooks.preprepush.timeoutSeconds":
		cfg.Hooks.PrePrePush.TimeoutSeconds = intVal
	case "push.retryAttempts":
		cfg.Push.RetryAttempts = intVal
	case "log.maxSizeMB":
		cfg.Log.MaxSizeMB = intVal
	default:
		return fmt.Errorf("unknown config key: %s", key)
	}
	return nil
}

// ValidConfigKeys returns the list of supported config keys.
func ValidConfigKeys() []string {
	return []string{
		"commit.casMaxAttempts",
		"lock.acquireTimeoutSeconds",
		"hooks.preprepush.timeoutSeconds",
		"push.retryAttempts",
		"log.maxSizeMB",
	}
}

func parseInt(s string) (int, error) {
	var v int
	_, err := fmt.Sscanf(s, "%d", &v)
	return v, err
}
