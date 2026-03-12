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
	config   *models.ProviderConfig
	baseURL  string
	apiKey   string
	log      *zap.Logger
}

func NewProvider(config *models.ProviderConfig, log *zap.Logger) (Provider, error) {
	if config.Type != "openai_compatible" {
		return nil, fmt.Errorf("unsupported provider type: %s", config.Type)
	}
	
	return &OpenAICompatibleProvider{
		config:  config,
		baseURL: strings.TrimSuffix(config.BaseURL, "/"),
		apiKey:  config.APIKey,
		log:     log,
	}, nil
}

func (p *OpenAICompatibleProvider) ChatCompletion(ctx context.Context, req models.ChatCompletionRequest) (*models.ChatCompletionResponse, error) {
	modelAlias := req.Model
	var upstreamModel string
	
	for _, m := range p.config.Models {
		if m.Alias == modelAlias {
			upstreamModel = m.Upstream
			break
		}
	}
	
	if upstreamModel == "" && len(p.config.Models) > 0 {
		upstreamModel = p.config.Models[0].Upstream
	} else if upstreamModel == "" {
		upstreamModel = modelAlias
	}
	
	req.Model = upstreamModel
	
	reqBytes, err := json.Marshal(req)
	if err != nil {
		return nil, err
	}
	
	p.log.Debug("Forwarding request to provider", 
		zap.String("model", req.Model),
		zap.Int("messages", len(req.Messages)),
		zap.String("base_url", p.baseURL))
	
	httpReq, err := http.NewRequestWithContext(ctx, "POST", p.baseURL+"/chat/completions", bytes.NewBuffer(reqBytes))
	if err != nil {
		return nil, err
	}
	
	httpReq.Header.Set("Content-Type", "application/json")
	if p.apiKey != "" {
		httpReq.Header.Set("Authorization", "Bearer "+p.apiKey)
	}
	
	client := &http.Client{Timeout: 30 * time.Second}
	resp, err := client.Do(httpReq)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, err
	}
	
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("provider error: status %d", resp.StatusCode)
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
