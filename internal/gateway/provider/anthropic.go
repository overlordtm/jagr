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

type AnthropicProvider struct {
	config     *models.ProviderConfig
	baseURL    string
	apiKey     string
	log        *zap.Logger
	httpClient *http.Client
}

var anthropicPricing = map[string]ModelPricing{
	// Claude 4 models (2025 pricing per million tokens)
	"claude-opus-4-20250514": {
		PromptCost:     15.00 / 1_000_000,
		CompletionCost: 75.00 / 1_000_000,
	},
	"claude-sonnet-4-20250514": {
		PromptCost:     3.00 / 1_000_000,
		CompletionCost: 15.00 / 1_000_000,
	},
	"claude-haiku-4-20250514": {
		PromptCost:     0.80 / 1_000_000,
		CompletionCost: 4.00 / 1_000_000,
	},
	// Claude 3.5 models
	"claude-3-5-sonnet-20241022": {
		PromptCost:     3.00 / 1_000_000,
		CompletionCost: 15.00 / 1_000_000,
	},
	"claude-3-5-haiku-20241022": {
		PromptCost:     0.80 / 1_000_000,
		CompletionCost: 4.00 / 1_000_000,
	},
	// Claude 3 models
	"claude-3-opus-20240229": {
		PromptCost:     15.00 / 1_000_000,
		CompletionCost: 75.00 / 1_000_000,
	},
	"claude-3-sonnet-20240229": {
		PromptCost:     3.00 / 1_000_000,
		CompletionCost: 15.00 / 1_000_000,
	},
	"claude-3-haiku-20240307": {
		PromptCost:     0.25 / 1_000_000,
		CompletionCost: 1.25 / 1_000_000,
	},
}

func NewAnthropicProvider(config *models.ProviderConfig, log *zap.Logger, timeouts *models.TimeoutConfig) (Provider, error) {
	baseURL := strings.TrimSuffix(config.BaseURL, "/")
	if baseURL == "" {
		baseURL = "https://api.anthropic.com/v1"
	}

	return &AnthropicProvider{
		config:     config,
		baseURL:    baseURL,
		apiKey:     config.APIKey,
		log:        log,
		httpClient: newHTTPClient(time.Duration(timeouts.ProviderTimeoutSeconds)*time.Second, config.TLSSkipVerify),
	}, nil
}

// Anthropic API request types
type anthropicMessage struct {
	Role    string             `json:"role"`
	Content []anthropicContent `json:"content"`
}

type anthropicContent struct {
	Type      string                 `json:"type"`
	Text      string                 `json:"text,omitempty"`
	ID        string                 `json:"id,omitempty"`
	Name      string                 `json:"name,omitempty"`
	Input     map[string]interface{} `json:"input,omitempty"`
	ToolUseID string                 `json:"tool_use_id,omitempty"`
	Content   string                 `json:"content,omitempty"`
}

type anthropicTool struct {
	Name        string                 `json:"name"`
	Description string                 `json:"description,omitempty"`
	InputSchema map[string]interface{} `json:"input_schema"`
}

type anthropicRequest struct {
	Model       string             `json:"model"`
	Messages    []anthropicMessage `json:"messages"`
	System      string             `json:"system,omitempty"`
	Tools       []anthropicTool    `json:"tools,omitempty"`
	MaxTokens   int                `json:"max_tokens"`
	Temperature float32            `json:"temperature,omitempty"`
	TopP        float32            `json:"top_p,omitempty"`
	TopK        int                `json:"top_k,omitempty"`
}

// Anthropic API response types
type anthropicResponse struct {
	ID         string             `json:"id"`
	Type       string             `json:"type"`
	Role       string             `json:"role"`
	Content    []anthropicContent `json:"content"`
	Model      string             `json:"model"`
	StopReason string             `json:"stop_reason"`
	Usage      anthropicUsage     `json:"usage"`
}

type anthropicUsage struct {
	InputTokens  int `json:"input_tokens"`
	OutputTokens int `json:"output_tokens"`
}

func (p *AnthropicProvider) ChatCompletion(ctx context.Context, req models.ChatCompletionRequest) (*models.ChatCompletionResponse, error) {
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

	anthropicReq, err := p.translateRequest(req, upstreamModel)
	if err != nil {
		return nil, fmt.Errorf("translating request: %w", err)
	}

	reqBytes, err := json.Marshal(anthropicReq)
	if err != nil {
		return nil, err
	}

	// Merge extra_body params
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

	p.log.Debug("Forwarding request to Anthropic",
		zap.String("model", upstreamModel),
		zap.Int("messages", len(req.Messages)),
		zap.String("base_url", p.baseURL))

	const maxRetries = 3
	var lastErr error

	for attempt := 0; attempt <= maxRetries; attempt++ {
		if attempt > 0 {
			backoff := time.Duration(1<<(attempt-1)) * time.Second
			p.log.Warn("Retrying Anthropic request after transient error",
				zap.Int("attempt", attempt),
				zap.Duration("backoff", backoff),
				zap.Error(lastErr))
			select {
			case <-time.After(backoff):
			case <-ctx.Done():
				return nil, ctx.Err()
			}
		}

		httpReq, err := http.NewRequestWithContext(ctx, "POST", p.baseURL+"/messages", bytes.NewBuffer(reqBytes))
		if err != nil {
			return nil, err
		}

		httpReq.Header.Set("Content-Type", "application/json")
		httpReq.Header.Set("x-api-key", p.apiKey)
		httpReq.Header.Set("anthropic-version", "2023-06-01")

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
			p.log.Error("Anthropic returned error",
				zap.Int("status", resp.StatusCode),
				zap.String("body", string(body)))
			lastErr = fmt.Errorf("anthropic error: status %d: %s", resp.StatusCode, string(body))
			continue
		}

		if resp.StatusCode != http.StatusOK {
			p.log.Error("Anthropic returned error",
				zap.Int("status", resp.StatusCode),
				zap.String("body", string(body)))
			return nil, fmt.Errorf("anthropic error: status %d: %s", resp.StatusCode, string(body))
		}

		var anthropicResp anthropicResponse
		if err := json.Unmarshal(body, &anthropicResp); err != nil {
			return nil, err
		}

		p.log.Debug("Got response from Anthropic",
			zap.String("model", anthropicResp.Model),
			zap.String("stop_reason", anthropicResp.StopReason))

		openAIResp := p.translateResponse(anthropicResp, upstreamModel)
		return openAIResp, nil
	}

	return nil, lastErr
}

func (p *AnthropicProvider) translateRequest(req models.ChatCompletionRequest, upstreamModel string) (*anthropicRequest, error) {
	anthropicReq := &anthropicRequest{
		Model:       upstreamModel,
		Messages:    []anthropicMessage{},
		MaxTokens:   4096, // Default, can be overridden
		Temperature: req.Temperature,
		TopP:        req.TopP,
		TopK:        req.TopK,
	}

	// Extract system message
	var systemMessages []string
	for _, msg := range req.Messages {
		if msg.Role == "system" {
			systemMessages = append(systemMessages, msg.Content)
		}
	}
	if len(systemMessages) > 0 {
		anthropicReq.System = strings.Join(systemMessages, "\n\n")
	}

	// Convert messages
	for _, msg := range req.Messages {
		if msg.Role == "system" {
			continue // Already handled
		}

		anthropicMsg := anthropicMessage{
			Role:    msg.Role,
			Content: []anthropicContent{},
		}

		// Handle tool calls
		if len(msg.ToolCalls) > 0 {
			// Assistant message with tool calls
			if msg.Content != "" {
				anthropicMsg.Content = append(anthropicMsg.Content, anthropicContent{
					Type: "text",
					Text: msg.Content,
				})
			}
			for _, tc := range msg.ToolCalls {
				input := map[string]interface{}{}
				if tc.Function.Arguments != "" {
					if err := json.Unmarshal([]byte(tc.Function.Arguments), &input); err != nil {
						return nil, fmt.Errorf("parsing tool arguments: %w", err)
					}
				}
				if input == nil {
					input = map[string]interface{}{}
				}
				anthropicMsg.Content = append(anthropicMsg.Content, anthropicContent{
					Type:  "tool_use",
					ID:    tc.ID,
					Name:  tc.Function.Name,
					Input: input,
				})
			}
		} else if msg.ToolCallID != "" {
			// Tool result message
			anthropicMsg.Role = "user"
			anthropicMsg.Content = append(anthropicMsg.Content, anthropicContent{
				Type:      "tool_result",
				ToolUseID: msg.ToolCallID,
				Content:   msg.Content,
			})
		} else {
			// Regular message
			if msg.Content != "" {
				anthropicMsg.Content = append(anthropicMsg.Content, anthropicContent{
					Type: "text",
					Text: msg.Content,
				})
			}
		}

		// Skip empty messages
		if len(anthropicMsg.Content) > 0 {
			anthropicReq.Messages = append(anthropicReq.Messages, anthropicMsg)
		}
	}

	// Convert tools
	if len(req.Tools) > 0 {
		anthropicReq.Tools = make([]anthropicTool, 0, len(req.Tools))
		for _, tool := range req.Tools {
			inputSchema, ok := tool.Function.Parameters.(map[string]interface{})
			if !ok {
				// Try to marshal and unmarshal to convert
				data, err := json.Marshal(tool.Function.Parameters)
				if err != nil {
					return nil, fmt.Errorf("marshaling tool parameters: %w", err)
				}
				if err := json.Unmarshal(data, &inputSchema); err != nil {
					return nil, fmt.Errorf("unmarshaling tool parameters: %w", err)
				}
			}

			anthropicReq.Tools = append(anthropicReq.Tools, anthropicTool{
				Name:        tool.Function.Name,
				Description: tool.Function.Description,
				InputSchema: inputSchema,
			})
		}
	}

	return anthropicReq, nil
}

// mapAnthropicStopReason converts Anthropic stop_reason values to OpenAI finish_reason values.
func mapAnthropicStopReason(reason string) string {
	switch reason {
	case "end_turn", "stop_sequence":
		return "stop"
	case "tool_use":
		return "tool_calls"
	case "max_tokens":
		return "length"
	default:
		return reason
	}
}

func (p *AnthropicProvider) translateResponse(resp anthropicResponse, model string) *models.ChatCompletionResponse {
	choice := models.Choice{
		Index:        0,
		FinishReason: mapAnthropicStopReason(resp.StopReason),
		Message: models.Message{
			Role:    "assistant",
			Content: "",
		},
	}

	var textParts []string
	var toolCalls []models.ToolCall

	for _, content := range resp.Content {
		switch content.Type {
		case "text":
			textParts = append(textParts, content.Text)
		case "tool_use":
			inputJSON, _ := json.Marshal(content.Input)
			toolCalls = append(toolCalls, models.ToolCall{
				ID:   content.ID,
				Type: "function",
				Function: models.Function{
					Name:      content.Name,
					Arguments: string(inputJSON),
				},
			})
		}
	}

	if len(textParts) > 0 {
		choice.Message.Content = strings.Join(textParts, "\n")
	}
	if len(toolCalls) > 0 {
		choice.Message.ToolCalls = toolCalls
	}

	// Calculate cost
	usage := &models.Usage{
		PromptTokens:     resp.Usage.InputTokens,
		CompletionTokens: resp.Usage.OutputTokens,
		TotalTokens:      resp.Usage.InputTokens + resp.Usage.OutputTokens,
	}

	if pricing, ok := anthropicPricing[model]; ok {
		promptCost := pricing.PromptCost * float64(resp.Usage.InputTokens)
		completionCost := pricing.CompletionCost * float64(resp.Usage.OutputTokens)
		totalCost := promptCost + completionCost
		usage.Cost = &totalCost

		p.log.Debug("Cost calculation",
			zap.String("model", model),
			zap.Int("input_tokens", resp.Usage.InputTokens),
			zap.Int("output_tokens", resp.Usage.OutputTokens),
			zap.Float64("prompt_cost", promptCost),
			zap.Float64("completion_cost", completionCost),
			zap.Float64("total_cost", totalCost))
	} else {
		p.log.Warn("No pricing found for model", zap.String("model", model))
	}

	return &models.ChatCompletionResponse{
		ID:      resp.ID,
		Object:  "chat.completion",
		Created: time.Now().Unix(),
		Model:   model,
		Choices: []models.Choice{choice},
		Usage:   usage,
	}
}
