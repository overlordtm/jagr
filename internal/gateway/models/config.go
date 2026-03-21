package models

type Config struct {
	Server    ServerConfig    `yaml:"server"`
	Database  DatabaseConfig  `yaml:"database"`
	RateLimit RateLimitConfig `yaml:"rate_limit"`
	Session   SessionConfig   `yaml:"session"`
	AgentAPIKey string        `yaml:"agent_api_key"`
	Providers []ProviderConfig `yaml:"providers"`
	DefaultProvider string     `yaml:"default_provider"`
	DefaultModel    string     `yaml:"default_model"`
	Logging   LoggingConfig  `yaml:"logging"`
}

type ServerConfig struct {
	Listen string       `yaml:"listen"`
	TLS    TLSConfig    `yaml:"tls"`
}

type TLSConfig struct {
	Cert string     `yaml:"cert"`
	Key  string     `yaml:"key"`
	ACME ACMEConfig `yaml:"acme,omitempty"`
}

type ACMEConfig struct {
	Enabled  bool     `yaml:"enabled"`
	Email    string   `yaml:"email"`
	Domains  []string `yaml:"domains"`
	CacheDir string   `yaml:"cache_dir"`
	CAUrl    string   `yaml:"ca_url,omitempty"`
}

type DatabaseConfig struct {
	Path string `yaml:"path"`
}

type RateLimitConfig struct {
	RequestsPerMinute int `yaml:"requests_per_minute"`
	MaxConcurrent     int `yaml:"max_concurrent"`
}

type SessionConfig struct {
	TimeoutMinutes      int    `yaml:"timeout_minutes"`
	HistoryMode         string `yaml:"history_mode"`
	MaxTokensPerSession int    `yaml:"max_tokens_per_session"`
}

type ProviderConfig struct {
	Name      string `yaml:"name"`
	Type      string `yaml:"type"`
	BaseURL   string `yaml:"base_url"`
	APIKey    string `yaml:"api_key"`
	Models    []ModelMapping `yaml:"models"`
}

type ModelMapping struct {
	Alias      string         `yaml:"alias"`
	Upstream   string         `yaml:"upstream"`
	ExtraBody  map[string]any `yaml:"extra_body,omitempty"`
}

type LoggingConfig struct {
	Level  string `yaml:"level"`
	Audit  bool   `yaml:"audit"`
}
