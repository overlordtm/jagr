package provider

import (
	"fmt"

	"go.uber.org/zap"

	"github.com/overlordtm/jagr/internal/gateway/models"
)

// NewProvider creates a provider instance based on the config type.
func NewProvider(config *models.ProviderConfig, log *zap.Logger, timeouts *models.TimeoutConfig) (Provider, error) {
	switch config.Type {
	case "anthropic":
		return NewAnthropicProvider(config, log, timeouts)
	case "openai_compatible":
		return NewOpenAICompatibleProvider(config, log, timeouts)
	default:
		return nil, fmt.Errorf("unsupported provider type: %s", config.Type)
	}
}
