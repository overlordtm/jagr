package provider

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"go.uber.org/zap"
	
	"github.com/overlordtm/jagr/internal/gateway/models"
)

type Provider interface {
	ChatCompletion(ctx context.Context, req models.ChatCompletionRequest) (*models.ChatCompletionResponse, error)
}

type OpenAICompatibleProvider struct {
	config          *models.ProviderConfig
	baseURL         string
	apiKey          string
	log             *zap.Logger
	Pricing         *PricingCache
	requestTimeout  time.Duration
}

func NewProvider(config *models.ProviderConfig, log *zap.Logger, timeouts *models.TimeoutConfig) (Provider, error) {
	if config.Type != "openai_compatible" {
		return nil, fmt.Errorf("unsupported provider type: %s", config.Type)
	}

	baseURL := strings.TrimSuffix(config.BaseURL, "/")
	pricingTimeout := time.Duration(timeouts.PricingFetchTimeoutSeconds) * time.Second
	pricing := NewPricingCache(baseURL, config.APIKey, log, pricingTimeout)

	if err := pricing.Fetch(); err != nil {
		log.Warn("Failed to fetch initial pricing (will retry)", zap.Error(err))
	}
	pricing.StartPeriodicRefresh(1 * time.Hour)

	return &OpenAICompatibleProvider{
		config:         config,
		baseURL:        baseURL,
		apiKey:         config.APIKey,
		log:            log,
		Pricing:        pricing,
		requestTimeout: time.Duration(timeouts.ProviderTimeoutSeconds) * time.Second,
	}, nil
}

func (p *OpenAICompatibleProvider) ChatCompletion(ctx context.Context, req models.ChatCompletionRequest) (*models.ChatCompletionResponse, error) {
	modelAlias := req.Model
	var upstreamModel string
	var extraBody map[string]any
	var maxMessages int

	for _, m := range p.config.Models {
		if m.Alias == modelAlias {
			upstreamModel = m.Upstream
			extraBody = m.ExtraBody
			maxMessages = m.MaxMessages
			break
		}
	}

	if upstreamModel == "" && len(p.config.Models) > 0 {
		upstreamModel = p.config.Models[0].Upstream
		extraBody = p.config.Models[0].ExtraBody
		maxMessages = p.config.Models[0].MaxMessages
	} else if upstreamModel == "" {
		upstreamModel = modelAlias
	}

	req.Model = upstreamModel

	if maxMessages > 0 && len(req.Messages) > maxMessages {
		req.Messages = trimMessages(req.Messages, maxMessages)
		p.log.Debug("Trimmed messages for model",
			zap.String("model", modelAlias),
			zap.Int("max_messages", maxMessages),
			zap.Int("trimmed_to", len(req.Messages)))
	}

	reqBytes, err := json.Marshal(req)
	if err != nil {
		return nil, err
	}

	// Merge extra_body params into the request JSON
	if len(extraBody) > 0 {
		var reqMap map[string]any
		if err := json.Unmarshal(reqBytes, &reqMap); err != nil {
			return nil, err
		}
		for k, v := range extraBody {
			reqMap[k] = v
		}
		reqBytes, err = json.Marshal(reqMap)
		if err != nil {
			return nil, err
		}
	}
	
	p.log.Debug("Forwarding request to provider",
		zap.String("model", req.Model),
		zap.Int("messages", len(req.Messages)),
		zap.String("base_url", p.baseURL))

	const maxRetries = 3
	const maxEmptyChoicesRetries = 1 // empty choices are rarely transient; fail fast
	var lastErr error
	var emptyChoicesAttempts int
	client := &http.Client{Timeout: p.requestTimeout}

	for attempt := 0; attempt <= maxRetries; attempt++ {
		if attempt > 0 {
			backoff := time.Duration(1<<(attempt-1)) * time.Second // 1s, 2s, 4s
			p.log.Warn("Retrying provider request after transient error",
				zap.Int("attempt", attempt),
				zap.Duration("backoff", backoff),
				zap.Error(lastErr))
			select {
			case <-time.After(backoff):
			case <-ctx.Done():
				return nil, ctx.Err()
			}
		}

		httpReq, err := http.NewRequestWithContext(ctx, "POST", p.baseURL+"/chat/completions", bytes.NewBuffer(reqBytes))
		if err != nil {
			return nil, err
		}

		httpReq.Header.Set("Content-Type", "application/json")
		if p.apiKey != "" {
			httpReq.Header.Set("Authorization", "Bearer "+p.apiKey)
		}

		resp, err := client.Do(httpReq)
		if err != nil {
			lastErr = err
			continue
		}

		body, err := io.ReadAll(resp.Body)
		resp.Body.Close()
		if err != nil {
			return nil, err
		}

		if resp.StatusCode >= 500 {
			p.log.Error("Provider returned error",
				zap.Int("status", resp.StatusCode),
				zap.String("body", string(body)))
			lastErr = fmt.Errorf("provider error: status %d: %s", resp.StatusCode, string(body))
			continue
		}

		if resp.StatusCode != http.StatusOK {
			p.log.Error("Provider returned error",
				zap.Int("status", resp.StatusCode),
				zap.String("body", string(body)))
			return nil, fmt.Errorf("provider error: status %d: %s", resp.StatusCode, string(body))
		}

		var reply models.ChatCompletionResponse
		if err := json.Unmarshal(body, &reply); err != nil {
			return nil, err
		}

		// OpenRouter sometimes returns HTTP 200 with an embedded error object
		if reply.Error != nil {
			p.log.Warn("Provider returned embedded error in 200 response",
				zap.String("message", reply.Error.Message),
				zap.Any("code", reply.Error.Code))
			lastErr = fmt.Errorf("provider error: %s (code: %v)", reply.Error.Message, reply.Error.Code)
			continue
		}

		// Empty choices with no error field — log body for diagnosis.
		// Only retry once since empty choices are rarely transient.
		if len(reply.Choices) == 0 {
			p.log.Warn("Provider returned empty choices",
				zap.String("model", reply.Model),
				zap.String("body", string(body)))
			lastErr = fmt.Errorf("provider returned empty choices for model %q", reply.Model)
			emptyChoicesAttempts++
			if emptyChoicesAttempts > maxEmptyChoicesRetries {
				return nil, lastErr
			}
			continue
		}

		p.log.Debug("Got response from provider",
			zap.String("model", reply.Model),
			zap.Int("choices", len(reply.Choices)))

		return &reply, nil
	}

	return nil, lastErr
}

// trimMessages keeps all leading system messages and the most recent non-system
// messages up to maxMessages total, snapping to a clean cut point so we never
// start mid-tool-sequence (i.e. never start on a "tool" role message).
func trimMessages(messages []models.Message, maxMessages int) []models.Message {
	// Collect leading system messages
	sysEnd := 0
	for sysEnd < len(messages) && messages[sysEnd].Role == "system" {
		sysEnd++
	}
	systemMsgs := messages[:sysEnd]
	rest := messages[sysEnd:]

	slots := maxMessages - len(systemMsgs)
	if slots <= 0 || len(rest) <= slots {
		return messages
	}

	// Cut from the oldest non-system messages
	cutFrom := len(rest) - slots

	// Snap forward past any orphaned tool-result messages so we never
	// start the trimmed window with a dangling tool response.
	for cutFrom < len(rest) && rest[cutFrom].Role == "tool" {
		cutFrom++
	}

	result := make([]models.Message, 0, len(systemMsgs)+len(rest)-cutFrom)
	result = append(result, systemMsgs...)
	result = append(result, rest[cutFrom:]...)
	return result
}
