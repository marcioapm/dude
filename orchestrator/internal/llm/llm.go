// Package llm is how the orchestrator speaks to the LLM proxy itself: which
// of the agent image's two providers a model goes through, the models the
// proxy lists, and a test message to one. It holds the proxy's key
// (DUDE_LLM_KEY); the backend never does.
package llm

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"sync"
	"time"
)

// The agent image's OpenCode providers (images/runtime/opencode.json): the
// Anthropic Messages API and an OpenAI-compatible Chat Completions API, both
// at DUDE_LLM_URL.
const (
	ProviderAnthropic = "llm-anthropic"
	ProviderOpenAI    = "llm-openai"
)

// Provider is the provider a model is requested through, by its name:
// Claude models speak Anthropic's API, every other model the
// OpenAI-compatible one. The one rule; the spec and the test message both
// follow it.
func Provider(model string) string {
	if strings.HasPrefix(model, "claude-") {
		return ProviderAnthropic
	}
	return ProviderOpenAI
}

// OpenAIEffort is a dude effort as OpenAI's reasoning_effort, whose scale
// stops at high: dude's "max" is the most it takes. The agent's config
// (phases.openCodeConfig) maps it the same way.
func OpenAIEffort(effort string) string {
	if effort == "max" {
		return "high"
	}
	return effort
}

// Client reaches the proxy at its base URL (…/v1) with dude's key.
type Client struct {
	URL, Key string
	HTTP     *http.Client
	// Bounds each test message; TestTimeout when zero.
	Timeout time.Duration
}

// TestTimeout bounds one test message.
const TestTimeout = 30 * time.Second

// testMaxTokens keeps a test message tiny: it checks the model answers,
// not what it says.
const testMaxTokens = 16

func (c Client) http() *http.Client {
	if c.HTTP != nil {
		return c.HTTP
	}
	return http.DefaultClient
}

func (c Client) endpoint(path string) string {
	return strings.TrimRight(c.URL, "/") + path
}

// Models are the model ids the proxy lists (GET /models, OpenAI's shape),
// in its order.
func (c Client) Models(ctx context.Context) ([]string, error) {
	if c.URL == "" {
		return nil, fmt.Errorf("the LLM proxy is not configured (DUDE_LLM_URL)")
	}
	ctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, c.endpoint("/models"), nil)
	if err != nil {
		return nil, err
	}
	if c.Key != "" {
		req.Header.Set("Authorization", "Bearer "+c.Key)
	}
	res, err := c.http().Do(req)
	if err != nil {
		return nil, fmt.Errorf("the LLM proxy could not be reached: %w", err)
	}
	defer res.Body.Close()
	body, _ := io.ReadAll(io.LimitReader(res.Body, 4<<20))
	if res.StatusCode/100 != 2 {
		return nil, fmt.Errorf("the LLM proxy answered %d: %s", res.StatusCode, errorMessage(body))
	}
	var list struct {
		Data []struct {
			ID string `json:"id"`
		} `json:"data"`
	}
	if err := json.Unmarshal(body, &list); err != nil {
		return nil, fmt.Errorf("the LLM proxy's model list is not JSON: %w", err)
	}
	out := make([]string, 0, len(list.Data))
	for _, m := range list.Data {
		if m.ID != "" {
			out = append(out, m.ID)
		}
	}
	return out, nil
}

// Result is one test message: at one effort ("" none), how long the proxy
// took, and what it answered.
type Result struct {
	Effort    *string `json:"effort"`
	OK        bool    `json:"ok"`
	LatencyMs int64   `json:"latencyMs"`
	// The proxy's HTTP status; nil when it did not answer.
	Status *int `json:"status"`
	// The proxy's error message as it sent it, or why no answer came.
	Error *string `json:"error"`
}

// Test sends one tiny request for model at each effort, together, in the
// wire format the agent uses for it: Anthropic Messages for Claude models,
// Chat Completions with reasoning_effort otherwise. OpenCode's Anthropic
// provider drops reasoningEffort (its option is named effort), so a Claude
// model is sent no effort here either: what is checked is what the agent
// will send.
func (c Client) Test(ctx context.Context, model string, efforts []string) []Result {
	out := make([]Result, len(efforts))
	var wg sync.WaitGroup
	for i, effort := range efforts {
		wg.Add(1)
		go func() {
			defer wg.Done()
			out[i] = c.test(ctx, model, effort)
		}()
	}
	wg.Wait()
	return out
}

func (c Client) test(ctx context.Context, model, effort string) Result {
	r := Result{}
	if effort != "" {
		r.Effort = &effort
	}
	timeout := c.Timeout
	if timeout == 0 {
		timeout = TestTimeout
	}
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	messages := []map[string]string{{"role": "user", "content": "Reply with the word OK."}}
	body := map[string]any{"model": model, "max_tokens": testMaxTokens, "messages": messages}
	path := "/chat/completions"
	headers := map[string]string{"Content-Type": "application/json"}
	if Provider(model) == ProviderAnthropic {
		path = "/messages"
		headers["x-api-key"] = c.Key
		headers["anthropic-version"] = "2023-06-01"
	} else {
		headers["Authorization"] = "Bearer " + c.Key
		if effort != "" {
			body["reasoning_effort"] = OpenAIEffort(effort)
		}
	}
	payload, _ := json.Marshal(body)
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.endpoint(path), bytes.NewReader(payload))
	if err != nil {
		msg := err.Error()
		r.Error = &msg
		return r
	}
	for k, v := range headers {
		req.Header.Set(k, v)
	}
	start := time.Now()
	res, err := c.http().Do(req)
	r.LatencyMs = time.Since(start).Milliseconds()
	if err != nil {
		msg := "no answer from the LLM proxy: " + err.Error()
		if ctx.Err() == context.DeadlineExceeded {
			msg = fmt.Sprintf("no answer from the LLM proxy in %s", timeout)
		}
		r.Error = &msg
		return r
	}
	defer res.Body.Close()
	raw, _ := io.ReadAll(io.LimitReader(res.Body, 1<<20))
	r.LatencyMs = time.Since(start).Milliseconds()
	status := res.StatusCode
	r.Status = &status
	if status/100 == 2 {
		r.OK = true
		return r
	}
	msg := errorMessage(raw)
	r.Error = &msg
	return r
}

// errorMessage is the message in an error body as the proxy sent it: an
// {"error":{"message"}} or {"error":"…"} body's message, else the body.
func errorMessage(body []byte) string {
	var shaped struct {
		Error   json.RawMessage `json:"error"`
		Message string          `json:"message"`
	}
	if json.Unmarshal(body, &shaped) == nil {
		var nested struct {
			Message string `json:"message"`
		}
		var flat string
		switch {
		case json.Unmarshal(shaped.Error, &nested) == nil && nested.Message != "":
			return nested.Message
		case json.Unmarshal(shaped.Error, &flat) == nil && flat != "":
			return flat
		case shaped.Message != "":
			return shaped.Message
		}
	}
	text := strings.TrimSpace(string(body))
	if len(text) > 2000 {
		text = text[:2000]
	}
	return text
}
