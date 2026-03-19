package config

import (
	"fmt"
	"os"
	"path/filepath"

	"github.com/spf13/viper"
)

const ConfigFileName = ".pgdelta.yml"

type Config struct {
	Version   int            `mapstructure:"version"`
	MainDB    DBConfig       `mapstructure:"main"`
	BranchDB  DBConfig       `mapstructure:"branch_db"`
	Branching BranchingConfig `mapstructure:"branching"`
	Snapshots SnapshotConfig `mapstructure:"snapshots"`
	PII       PIIConfig      `mapstructure:"pii"`
	AI        AIConfig       `mapstructure:"ai"`
	Git       GitConfig      `mapstructure:"git"`
	Telemetry TelemetryConfig `mapstructure:"telemetry"`
}

type DBConfig struct {
	URL    string `mapstructure:"url"`
	Access string `mapstructure:"access"` // readonly | readwrite
}

type BranchingConfig struct {
	SchemaFrom  string `mapstructure:"schema_from"`  // git-parent
	AutoCreate  bool   `mapstructure:"auto_create"`
	AutoCleanup bool   `mapstructure:"auto_cleanup"`
	AutoMerge   bool   `mapstructure:"auto_merge"`
}

type SnapshotConfig struct {
	DefaultRowLimit int                       `mapstructure:"default_row_limit"`
	ChunkSize       int                       `mapstructure:"chunk_size"`
	ParallelTables  int                       `mapstructure:"parallel_tables"`
	ResumeOnInterrupt bool                    `mapstructure:"resume_on_interrupt"`
	Tables          map[string]TableSnapshot  `mapstructure:"tables"`
	Exclude         []string                  `mapstructure:"exclude"`
}

type TableSnapshot struct {
	Query string `mapstructure:"query"`
	Limit int    `mapstructure:"limit"`
}

type PIIConfig struct {
	AutoDetect bool                       `mapstructure:"auto_detect"`
	Masks      map[string]interface{}     `mapstructure:"masks"`
}

// GetMasks returns PII masks as flat map[string]string
// handles both "table.column: value" and nested yaml formats
func (p *PIIConfig) GetMasks() map[string]string {
	result := make(map[string]string)
	for k, v := range p.Masks {
		switch val := v.(type) {
		case string:
			result[k] = val
		case map[string]interface{}:
			// nested format: users: {email: mask}
			for col, mask := range val {
				if maskStr, ok := mask.(string); ok {
					result[k+"."+col] = maskStr
				}
			}
		}
	}
	return result
}

type AIConfig struct {
	Enabled  bool   `mapstructure:"enabled"`
	Provider string `mapstructure:"provider"`
	APIKey   string `mapstructure:"api_key"`
	Features AIFeatures `mapstructure:"features"`
}

type AIFeatures struct {
	ExtractionSuggestions bool `mapstructure:"extraction_suggestions"`
	MigrationRisk         bool `mapstructure:"migration_risk"`
	ConflictResolution    bool `mapstructure:"conflict_resolution"`
	PIIDetection          bool `mapstructure:"pii_detection"`
}

type GitConfig struct {
	Hooks      bool `mapstructure:"hooks"`
	StatusLine bool `mapstructure:"status_line"`
}

type TelemetryConfig struct {
	Enabled   bool `mapstructure:"enabled"`
	Anonymous bool `mapstructure:"anonymous"`
}

// Load reads .pgdelta.yml from the current directory or any parent directory
func Load() (*Config, error) {
	v := viper.New()

	// Set defaults
	v.SetDefault("version", 1)
	v.SetDefault("main.access", "readonly")
	v.SetDefault("branch_db.access", "readwrite")
	v.SetDefault("branching.schema_from", "git-parent")
	v.SetDefault("branching.auto_create", true)
	v.SetDefault("branching.auto_cleanup", true)
	v.SetDefault("branching.auto_merge", true)
	v.SetDefault("snapshots.default_row_limit", 10000)
	v.SetDefault("snapshots.chunk_size", 10000)
	v.SetDefault("snapshots.parallel_tables", 3)
	v.SetDefault("snapshots.resume_on_interrupt", true)
	v.SetDefault("ai.enabled", true)
	v.SetDefault("ai.provider", "anthropic")
	v.SetDefault("ai.features.extraction_suggestions", true)
	v.SetDefault("ai.features.migration_risk", true)
	v.SetDefault("ai.features.conflict_resolution", true)
	v.SetDefault("ai.features.pii_detection", true)
	v.SetDefault("git.hooks", true)
	v.SetDefault("git.status_line", true)
	v.SetDefault("telemetry.enabled", true)
	v.SetDefault("telemetry.anonymous", true)

	// Allow environment variable overrides
	v.AutomaticEnv()

	// Find config file walking up from current directory
	configPath, err := findConfigFile()
	if err != nil {
		return nil, fmt.Errorf("config file not found: run 'pgdelta init' first")
	}

	v.SetConfigFile(configPath)

	if err := v.ReadInConfig(); err != nil {
		return nil, fmt.Errorf("failed to read config: %w", err)
	}

	var cfg Config
	if err := v.Unmarshal(&cfg); err != nil {
		return nil, fmt.Errorf("failed to parse config: %w", err)
	}

	// Expand environment variables in URLs
	cfg.MainDB.URL = os.ExpandEnv(cfg.MainDB.URL)
	cfg.BranchDB.URL = os.ExpandEnv(cfg.BranchDB.URL)
	cfg.AI.APIKey = os.ExpandEnv(cfg.AI.APIKey)

	return &cfg, nil
}

// findConfigFile walks up the directory tree looking for .pgdelta.yml
func findConfigFile() (string, error) {
	dir, err := os.Getwd()
	if err != nil {
		return "", err
	}

	for {
		candidate := filepath.Join(dir, ConfigFileName)
		if _, err := os.Stat(candidate); err == nil {
			return candidate, nil
		}

		parent := filepath.Dir(dir)
		if parent == dir {
			// Reached filesystem root
			return "", fmt.Errorf("not found")
		}
		dir = parent
	}
}

// WriteDefault writes a default .pgdelta.yml to the current directory
func WriteDefault(mainURL, branchURL string) error {
	content := fmt.Sprintf(`version: 1

# Main database — read only, source of truth
main:
  url: %s
  access: readonly

# Branch database — pgDelta owns this completely
branch_db:
  url: %s
  access: readwrite

# Branching behavior
branching:
  schema_from: git-parent
  auto_create: true
  auto_cleanup: true
  auto_merge: true

# Data snapshot configuration
snapshots:
  default_row_limit: 10000
  chunk_size: 10000
  parallel_tables: 3
  resume_on_interrupt: true
  tables: {}
  exclude:
    - audit_logs
    - sessions
    - password_reset_tokens

# PII masking
pii:
  auto_detect: true
  masks: {}

# AI configuration
ai:
  enabled: true
  provider: anthropic
  api_key: ${ANTHROPIC_API_KEY}
  features:
    extraction_suggestions: true
    migration_risk: true
    conflict_resolution: true
    pii_detection: true

# Git integration
git:
  hooks: true
  status_line: true

# Anonymous telemetry — helps AI get smarter
telemetry:
  enabled: true
  anonymous: true
`, mainURL, branchURL)

	return os.WriteFile(ConfigFileName, []byte(content), 0644)
}

// Exists checks if a config file exists in or above the current directory
func Exists() bool {
	_, err := findConfigFile()
	return err == nil
}