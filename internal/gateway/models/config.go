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
	Dashboard DashboardConfig `yaml:"dashboard"`
	Timeouts  TimeoutConfig  `yaml:"timeouts"`
	Knowledge *LightRAGConfig `yaml:"knowledge,omitempty"`
	DefaultMaxIterations int                    `yaml:"default_max_iterations,omitempty"`
	Agents               map[string]AgentProfile `yaml:"agents,omitempty"`
	SkillsDir            string                  `yaml:"skills_dir,omitempty"`
}

type LightRAGConfig struct {
	BaseURL string `yaml:"base_url"`
	APIKey  string `yaml:"api_key"`
}

type AgentProfile struct {
	Model         string  `yaml:"model" json:"model"`
	Temperature   float32 `yaml:"temperature,omitempty" json:"temperature,omitempty"`
	TopP          float32 `yaml:"top_p,omitempty" json:"top_p,omitempty"`
	TopK          int     `yaml:"top_k,omitempty" json:"top_k,omitempty"`
	MaxIterations int     `yaml:"max_iterations,omitempty" json:"max_iterations,omitempty"`
}

type DashboardConfig struct {
	Enabled bool              `yaml:"enabled"`
	Listen  string            `yaml:"listen"`
	Users   []DashboardUser   `yaml:"users,omitempty"`
	OIDC    *OIDCConfig       `yaml:"oidc,omitempty"`
}

type DashboardUser struct {
	Username string `yaml:"username"`
	Password string `yaml:"password"`
}

// OIDCConfig configures Keycloak (or any OIDC-compliant) authentication for the dashboard.
type OIDCConfig struct {
	// IssuerURL is the Keycloak realm URL, e.g. https://keycloak.example.com/realms/myrealm
	IssuerURL    string `yaml:"issuer_url"`
	ClientID     string `yaml:"client_id"`
	ClientSecret string `yaml:"client_secret"`
	// RedirectURL must match the redirect URI registered in Keycloak,
	// e.g. https://dashboard.example.com/auth/callback
	RedirectURL  string `yaml:"redirect_url"`
	// SessionTTLMinutes is how long a dashboard session lasts (default: 480 = 8h)
	SessionTTLMinutes int `yaml:"session_ttl_minutes,omitempty"`
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
	TimeoutMinutes      int `yaml:"timeout_minutes"`
	MaxTokensPerSession int `yaml:"max_tokens_per_session"`
}

type TimeoutConfig struct {
	ProviderTimeoutSeconds      int `yaml:"provider_timeout_seconds"`
	PricingFetchTimeoutSeconds  int `yaml:"pricing_fetch_timeout_seconds"`
}

type ProviderConfig struct {
	Name      string `yaml:"name"`
	Type      string `yaml:"type"`
	BaseURL   string `yaml:"base_url"`
	APIKey    string `yaml:"api_key"`
	Models    []ModelMapping `yaml:"models"`
}

type ModelMapping struct {
	Alias            string         `yaml:"alias"`
	Upstream         string         `yaml:"upstream"`
	MaxContextWindow int            `yaml:"max_context_window,omitempty"`
	ExtraBody        map[string]any `yaml:"extra_body,omitempty"`
	// MaxMessages limits how many messages are sent per request (0 = unlimited).
	// Useful for models that return empty choices on long tool-use conversations.
	// When the conversation exceeds this, the oldest non-system messages are dropped.
	MaxMessages      int            `yaml:"max_messages,omitempty"`
}

type LoggingConfig struct {
	Level  string `yaml:"level"`
	Audit  bool   `yaml:"audit"`
}
