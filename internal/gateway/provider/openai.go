package provider

import (
	"bytes"
	"context"
	"crypto/tls"
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
	config     *models.ProviderConfig
	baseURL    string
	apiKey     string
	log        *zap.Logger
	Pricing    *PricingCache
	httpClient *http.Client
}

func NewOpenAICompatibleProvider(config *models.ProviderConfig, log *zap.Logger, timeouts *models.TimeoutConfig) (*OpenAICompatibleProvider, error) {
	baseURL := strings.TrimSuffix(config.BaseURL, "/")
	requestTimeout := time.Duration(timeouts.ProviderTimeoutSeconds) * time.Second
	pricingTimeout := time.Duration(timeouts.PricingFetchTimeoutSeconds) * time.Second

	httpClient := newHTTPClient(requestTimeout, config.TLSSkipVerify)
	pricing := NewPricingCache(baseURL, config.APIKey, log, pricingTimeout, config.TLSSkipVerify)

	if err := pricing.Fetch(); err != nil {
		log.Warn("Failed to fetch initial pricing (will retry)", zap.Error(err))
	}
	pricing.StartPeriodicRefresh(1 * time.Hour)

	return &OpenAICompatibleProvider{
		config:     config,
		baseURL:    baseURL,
		apiKey:     config.APIKey,
		log:        log,
		Pricing:    pricing,
		httpClient: httpClient,
	}, nil
}

func newHTTPClient(timeout time.Duration, tlsSkipVerify bool) *http.Client {
	transport := http.DefaultTransport.(*http.Transport).Clone()
	if tlsSkipVerify {
		transport.TLSClientConfig = &tls.Config{InsecureSkipVerify: true} //nolint:gosec // user-opted-in
	}
	return &http.Client{Timeout: timeout, Transport: transport}
}

func (p *OpenAICompatibleProvider) ChatCompletion(ctx context.Context, req models.ChatCompletionRequest) (*models.ChatCompletionResponse, error) {
	modelAlias := req.Model
	var upstreamModel string
	var extraBody map[string]any

	for _, m := range p.config.Models {
		if m.Alias == modelAlias {
			upstreamModel = m.Upstream
			extraBody = m.ExtraBody
			break
		}
	}

	if upstreamModel == "" && len(p.config.Models) > 0 {
		upstreamModel = p.config.Models[0].Upstream
		extraBody = p.config.Models[0].ExtraBody
	} else if upstreamModel == "" {
		upstreamModel = modelAlias
	}

	req.Model = upstreamModel

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
	var lastErr error

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

		resp, err := p.httpClient.Do(httpReq)
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

		p.log.Debug("Got response from provider",
			zap.String("model", reply.Model),
			zap.Int("choices", len(reply.Choices)))

		return &reply, nil
	}

	return nil, lastErr
}
