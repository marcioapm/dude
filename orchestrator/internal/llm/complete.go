package llm

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"
)

// CompleteTimeout bounds one Complete call when its Call names none.
const CompleteTimeout = 60 * time.Second

// Message is one chat message as Chat Completions takes it.
type Message struct {
	Role    string `json:"role"`
	Content string `json:"content"`
}

// Call is one request to a tier's model, with no tools and no schema.
type Call struct {
	Model string
	// The tier's reasoning effort; "" sends none (the model's default).
	Effort string
	// The tier's request headers.
	Headers  map[string]string
	Messages []Message
	// 0 leaves max_tokens out.
	MaxTokens   int
	Temperature *float64
	// 0 is CompleteTimeout.
	Timeout time.Duration
}

// Completion is what the proxy answered, as far as it got: a failed call
// still carries its latency and, when the proxy answered, its status.
type Completion struct {
	Text         string
	FinishReason string
	// The model the proxy says served the call.
	ServedModel  string
	InputTokens  int
	OutputTokens int
	LatencyMs    int64
	// The proxy's HTTP status; nil when it did not answer.
	Status *int
}

// Body is the request body Complete sends for call, as JSON would carry
// it: what a caller records as "what was sent".
func (call Call) Body() map[string]any {
	body := map[string]any{"model": call.Model, "messages": call.Messages, "stream": false}
	if call.MaxTokens > 0 {
		body["max_tokens"] = call.MaxTokens
	}
	if call.Temperature != nil {
		body["temperature"] = *call.Temperature
	}
	// The tier's OpenCode (AI SDK) options are not applied: they are named
	// for the agent's providers, not for Chat Completions.
	if call.Effort != "" {
		body["reasoning_effort"] = call.Effort
	}
	return body
}

// Complete sends one OpenAI-compatible Chat Completions request
// (POST {URL}/chat/completions) and returns the first choice's text. The
// proxy translates it for every provider, so nothing here depends on the
// model's name. On error the Completion holds what was learnt before it.
func (c Client) Complete(ctx context.Context, call Call) (Completion, error) {
	var out Completion
	if c.URL == "" {
		return out, errors.New("the LLM proxy is not configured (DUDE_LLM_URL)")
	}
	timeout := call.Timeout
	if timeout == 0 {
		timeout = CompleteTimeout
	}
	parent := ctx
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	payload, err := json.Marshal(call.Body())
	if err != nil {
		return out, err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.endpoint("/chat/completions"), bytes.NewReader(payload))
	if err != nil {
		return out, err
	}
	for k, v := range call.Headers {
		req.Header.Set(k, v)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+c.Key)
	start := time.Now()
	fail := func(err error) (Completion, error) {
		out.LatencyMs = time.Since(start).Milliseconds()
		if ctx.Err() == context.DeadlineExceeded {
			if parent.Err() == nil {
				return out, fmt.Errorf("no answer from the LLM proxy in %s", timeout)
			}
			return out, errors.New("no answer from the LLM proxy before the caller's deadline")
		}
		return out, fmt.Errorf("no answer from the LLM proxy: %w", err)
	}
	res, err := c.http().Do(req)
	if err != nil {
		return fail(err)
	}
	defer res.Body.Close()
	raw, err := io.ReadAll(io.LimitReader(res.Body, 4<<20))
	if err != nil {
		return fail(err)
	}
	out.LatencyMs = time.Since(start).Milliseconds()
	status := res.StatusCode
	out.Status = &status
	if status/100 != 2 {
		return out, fmt.Errorf("the LLM proxy answered %d: %s", status, errorMessage(raw))
	}
	var parsed struct {
		Model   string `json:"model"`
		Choices []struct {
			FinishReason string `json:"finish_reason"`
			Message      struct {
				Content json.RawMessage `json:"content"`
			} `json:"message"`
		} `json:"choices"`
		Usage struct {
			PromptTokens     int `json:"prompt_tokens"`
			CompletionTokens int `json:"completion_tokens"`
		} `json:"usage"`
	}
	if err := json.Unmarshal(raw, &parsed); err != nil {
		return out, fmt.Errorf("the LLM proxy's answer is not JSON: %w", err)
	}
	out.ServedModel = parsed.Model
	out.InputTokens = parsed.Usage.PromptTokens
	out.OutputTokens = parsed.Usage.CompletionTokens
	if len(parsed.Choices) == 0 {
		return out, errors.New("the LLM proxy answered with no choices")
	}
	out.FinishReason = parsed.Choices[0].FinishReason
	out.Text = contentText(parsed.Choices[0].Message.Content)
	return out, nil
}

// contentText is a message's content as text: the string, or an array's
// text parts joined (some providers answer in parts).
func contentText(raw json.RawMessage) string {
	var s string
	if json.Unmarshal(raw, &s) == nil {
		return s
	}
	var parts []struct {
		Type string `json:"type"`
		Text string `json:"text"`
	}
	if json.Unmarshal(raw, &parts) != nil {
		return ""
	}
	var b strings.Builder
	for _, p := range parts {
		if p.Type == "text" || p.Type == "" {
			b.WriteString(p.Text)
		}
	}
	return b.String()
}
