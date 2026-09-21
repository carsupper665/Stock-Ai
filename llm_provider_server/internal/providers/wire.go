package providers

import (
	"encoding/json"
	"regexp"
)

var ModelNamePattern = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9_.-]{0,127}$`)
var compatibleToolNamePattern = regexp.MustCompile(`^[A-Za-z0-9_-]{1,64}$`)
var ProviderIDPattern = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9_-]{0,63}$`)
var OpenAIOptionNames = map[string]bool{
	"frequency_penalty": true, "logit_bias": true, "logprobs": true, "max_completion_tokens": true,
	"max_tokens": true, "metadata": true, "parallel_tool_calls": true, "prediction": true,
	"presence_penalty": true, "prompt_cache_key": true, "prompt_cache_options": true,
	"prompt_cache_retention": true, "reasoning_effort": true, "response_format": true,
	"safety_identifier": true, "seed": true, "service_tier": true, "stop": true, "store": true,
	"temperature": true, "tool_choice": true, "top_logprobs": true, "top_p": true, "user": true,
	"verbosity": true,
}

type GenerateRequest struct {
	Provider       string                     `json:"provider"`
	Model          *string                    `json:"model"`
	Messages       []Message                  `json:"messages"`
	Tools          []ToolSchema               `json:"tools"`
	Options        map[string]json.RawMessage `json:"options"`
	ConversationID *string                    `json:"conversation_id,omitempty"`
	Continuation   *GenerateContinuation      `json:"continuation,omitempty"`
}

type GenerateContinuation struct {
	AfterMessages int       `json:"after_messages"`
	Messages      []Message `json:"messages"`
}

type Message struct {
	Role       string     `json:"role"`
	Content    *string    `json:"content"`
	ToolCalls  []ToolCall `json:"tool_calls,omitempty"`
	ToolCallID *string    `json:"tool_call_id,omitempty"`
}

type ToolSchema struct {
	Name        string          `json:"name"`
	Description string          `json:"description"`
	InputSchema json.RawMessage `json:"input_schema"`
}

type ToolCall struct {
	ID        string          `json:"id"`
	Name      string          `json:"name"`
	Arguments json.RawMessage `json:"arguments"`
}

type ToolAliases struct {
	WireByRuntime map[string]string
	RuntimeByWire map[string]string
}

type ProviderCall struct {
	URL     string
	Headers map[string]string
	Body    []byte
}

// ProviderError is a Provider status already classified into the §22 envelope.
type ProviderError struct {
	Status  int
	Code    string
	Message string
}

func (e ProviderError) Error() string { return e.Message }

type Usage struct {
	InputTokens  *int `json:"input_tokens"`
	OutputTokens *int `json:"output_tokens"`
	CachedTokens *int `json:"cached_tokens"`
	TotalTokens  *int `json:"total_tokens"`
}

type GenerateResponse struct {
	Content         *string    `json:"content"`
	Reasoning       *string    `json:"reasoning,omitempty"`
	ToolCalls       []ToolCall `json:"tool_calls"`
	FinishReason    string     `json:"finish_reason"`
	Usage           *Usage     `json:"usage"`
	NumTurns        *int       `json:"num_turns,omitempty"`
	TotalCostUSD    *float64   `json:"total_cost_usd,omitempty"`
	SessionMode     string     `json:"session_mode,omitempty"`
	FallbackReason  string     `json:"fallback_reason,omitempty"`
	InvocationCount int        `json:"invocation_count,omitempty"`
}

// SumTokens adds the Usage components a Provider actually reported; it is null only when it reported none.
func SumTokens(components ...*int) *int {
	var total *int
	for _, component := range components {
		if component == nil {
			continue
		}
		if total == nil {
			total = new(int)
		}
		*total += *component
	}
	return total
}

func negativeTokens(counts ...*int) bool {
	for _, count := range counts {
		if count != nil && *count < 0 {
			return true
		}
	}
	return false
}
