package provider

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"sync"
	"time"

	"go.uber.org/zap"
)

// ModelPricing holds per-token costs for a model.
type ModelPricing struct {
	PromptCost     float64 // cost per token (prompt/input)
	CompletionCost float64 // cost per token (completion/output)
}

// PricingCache fetches and caches model pricing from OpenRouter.
type PricingCache struct {
	mu         sync.RWMutex
	prices     map[string]ModelPricing
	baseURL    string
	apiKey     string
	log        *zap.Logger
	httpClient *http.Client
}

func NewPricingCache(baseURL, apiKey string, log *zap.Logger, fetchTimeout time.Duration, tlsSkipVerify bool) *PricingCache {
	return &PricingCache{
		prices:     make(map[string]ModelPricing),
		baseURL:    baseURL,
		apiKey:     apiKey,
		log:        log,
		httpClient: newHTTPClient(fetchTimeout, tlsSkipVerify),
	}
}

// openRouterModel is the relevant subset of the OpenRouter /api/v1/models response.
type openRouterModel struct {
	ID      string `json:"id"`
	Pricing *struct {
		Prompt     string `json:"prompt"`
		Completion string `json:"completion"`
	} `json:"pricing"`
}

type openRouterModelsResponse struct {
	Data []openRouterModel `json:"data"`
}

// Fetch retrieves pricing for all models from OpenRouter and caches it.
func (pc *PricingCache) Fetch() error {
	url := pc.baseURL + "/models"

	req, err := http.NewRequest("GET", url, nil)
	if err != nil {
		return fmt.Errorf("creating request: %w", err)
	}
	if pc.apiKey != "" {
		req.Header.Set("Authorization", "Bearer "+pc.apiKey)
	}

	resp, err := pc.httpClient.Do(req)
	if err != nil {
		return fmt.Errorf("fetching models: %w", err)
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return fmt.Errorf("reading response: %w", err)
	}

	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("models endpoint returned status %d: %s", resp.StatusCode, string(body))
	}

	var modelsResp openRouterModelsResponse
	if err := json.Unmarshal(body, &modelsResp); err != nil {
		return fmt.Errorf("parsing models response: %w", err)
	}

	prices := make(map[string]ModelPricing, len(modelsResp.Data))
	for _, m := range modelsResp.Data {
		if m.Pricing == nil {
			continue
		}
		promptCost, _ := strconv.ParseFloat(m.Pricing.Prompt, 64)
		completionCost, _ := strconv.ParseFloat(m.Pricing.Completion, 64)
		prices[m.ID] = ModelPricing{
			PromptCost:     promptCost,
			CompletionCost: completionCost,
		}
	}

	pc.mu.Lock()
	pc.prices = prices
	pc.mu.Unlock()

	pc.log.Info("Fetched model pricing", zap.Int("models", len(prices)))
	return nil
}

// Get returns the pricing for a model. Returns zero pricing if not found.
func (pc *PricingCache) Get(model string) (ModelPricing, bool) {
	pc.mu.RLock()
	defer pc.mu.RUnlock()
	p, ok := pc.prices[model]
	return p, ok
}

// CalculateCost computes the cost for a request given token counts.
func (pc *PricingCache) CalculateCost(model string, promptTokens, completionTokens int) (promptCost, completionCost, totalCost float64) {
	pricing, ok := pc.Get(model)
	if !ok {
		return 0, 0, 0
	}
	promptCost = pricing.PromptCost * float64(promptTokens)
	completionCost = pricing.CompletionCost * float64(completionTokens)
	totalCost = promptCost + completionCost
	return
}

// StartPeriodicRefresh refreshes pricing in the background at the given interval.
func (pc *PricingCache) StartPeriodicRefresh(interval time.Duration) {
	go func() {
		ticker := time.NewTicker(interval)
		defer ticker.Stop()
		for range ticker.C {
			if err := pc.Fetch(); err != nil {
				pc.log.Warn("Failed to refresh pricing", zap.Error(err))
			}
		}
	}()
}
