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
	"maps"
	"net/http"
	"strings"
	"time"
)

// The agent image's OpenCode providers (images/runtime/opencode.json): the
// Anthropic Messages API and the OpenAI Responses API, both at DUDE_LLM_URL.
const (
	ProviderAnthropic = "llm-anthropic"
	ProviderOpenAI    = "llm-openai"
)

// Provider is the provider a model is requested through, by its name:
// Claude models speak Anthropic's API, every other model OpenAI's. The one
// rule; the spec and the test message both follow it.
func Provider(model string) string {
	if strings.HasPrefix(model, "claude-") {
		return ProviderAnthropic
	}
	return ProviderOpenAI
}

// Efforts are a tier's reasoning efforts; "" (NULL) is the model's default.
var Efforts = []string{"none", "low", "medium", "high", "max"}

// ModelOptions are the OpenCode model options (AI SDK provider options) a
// tier's agent is requested with: its effort through model's provider,
// with the tier's own options deep-merged over them (the tier's win). The
// one place dude knows a provider's option names; the agent's config
// (phases.openCodeConfig) and the test message both take them from here.
func ModelOptions(model, effort string, extra map[string]any) map[string]any {
	out := map[string]any{}
	switch Provider(model) {
	case ProviderAnthropic:
		if effort == "none" {
			out["thinking"] = map[string]any{"type": "disabled"}
			break
		}
		// Claude 5.x sends its thinking blocks with empty text unless the
		// request asks for a summary of them.
		out["thinking"] = map[string]any{"type": "adaptive", "display": "summarized"}
		if effort != "" {
			// Sent as output_config.effort.
			out["effort"] = effort
		}
	default:
		// The Responses API returns reasoning text only as a summary.
		out["reasoningSummary"] = "auto"
		if effort != "" && effort != "none" {
			// Sent as reasoning.effort; Responses takes max.
			out["reasoningEffort"] = effort
		}
	}
	return mergeOptions(out, extra)
}

// mergeOptions deep-merges over onto base, in place: objects merge key by
// key, anything else in over replaces what base had. Returns base.
func mergeOptions(base, over map[string]any) map[string]any {
	for k, v := range over {
		if sub, ok := v.(map[string]any); ok {
			if have, ok := base[k].(map[string]any); ok {
				base[k] = mergeOptions(maps.Clone(have), sub)
				continue
			}
		}
		base[k] = v
	}
	return base
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

// Result is one test message: how long the proxy took, and what it
// answered.
type Result struct {
	// The tier's reasoning settings as they went on the wire: Anthropic's
	// thinking and output_config, or the Responses API's reasoning.
	Sent      map[string]any `json:"sent"`
	OK        bool           `json:"ok"`
	LatencyMs int64          `json:"latencyMs"`
	// The proxy's HTTP status; nil when it did not answer.
	Status *int `json:"status"`
	// The proxy's error message as it sent it, or why no answer came.
	Error *string `json:"error"`
}

// TestTier is what a test message is sent for: a tier's model, effort,
// extra OpenCode model options and request headers.
type TestTier struct {
	Model, Effort string
	Options       map[string]any
	Headers       map[string]string
}

// wireReasoning is the part of a request body ModelOptions' reasoning keys
// become, as the agent's AI SDK provider writes them: Anthropic's thinking
// and effort (output_config.effort); OpenAI's reasoningEffort and
// reasoningSummary (reasoning.effort, reasoning.summary). Other option keys
// are not sent: what the provider makes of them is the provider's.
func wireReasoning(model string, options map[string]any) map[string]any {
	out := map[string]any{}
	if Provider(model) == ProviderAnthropic {
		if v, ok := options["thinking"]; ok {
			out["thinking"] = v
		}
		if v, ok := options["effort"]; ok {
			out["output_config"] = map[string]any{"effort": v}
		}
		return out
	}
	reasoning := map[string]any{}
	if v, ok := options["reasoningEffort"]; ok {
		reasoning["effort"] = v
	}
	if v, ok := options["reasoningSummary"]; ok {
		reasoning["summary"] = v
	}
	if len(reasoning) > 0 {
		out["reasoning"] = reasoning
	}
	return out
}

// Test sends one tiny request for a tier's model as its agent sends it:
// Anthropic Messages with the tier's thinking and effort for a Claude
// model, else the Responses API with its reasoning, streamed (as the agent
// streams; the proxy takes Responses only so). The tier's headers go too.
func (c Client) Test(ctx context.Context, t TestTier) Result {
	sent := wireReasoning(t.Model, ModelOptions(t.Model, t.Effort, t.Options))
	r := Result{Sent: sent}
	timeout := c.Timeout
	if timeout == 0 {
		timeout = TestTimeout
	}
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	messages := []map[string]string{{"role": "user", "content": "Reply with the word OK."}}
	body := map[string]any{"model": t.Model}
	headers := map[string]string{}
	maps.Copy(headers, t.Headers)
	headers["Content-Type"] = "application/json"
	anthropic := Provider(t.Model) == ProviderAnthropic
	path := "/responses"
	if anthropic {
		path = "/messages"
		body["max_tokens"] = testMaxTokens
		body["messages"] = messages
		headers["x-api-key"] = c.Key
		headers["anthropic-version"] = "2023-06-01"
	} else {
		body["max_output_tokens"] = testMaxTokens
		body["input"] = messages
		body["stream"] = true
		body["store"] = false
		headers["Authorization"] = "Bearer " + c.Key
	}
	maps.Copy(body, sent)
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
	fail := func(err error) Result {
		msg := "no answer from the LLM proxy: " + err.Error()
		if ctx.Err() == context.DeadlineExceeded {
			msg = fmt.Sprintf("no answer from the LLM proxy in %s", timeout)
		}
		r.Error = &msg
		return r
	}
	if err != nil {
		return fail(err)
	}
	defer res.Body.Close()
	raw, err := io.ReadAll(io.LimitReader(res.Body, 1<<20))
	r.LatencyMs = time.Since(start).Milliseconds()
	if err != nil {
		return fail(err)
	}
	status := res.StatusCode
	r.Status = &status
	if status/100 != 2 {
		msg := errorMessage(raw)
		r.Error = &msg
		return r
	}
	if !anthropic {
		// A refusal of the request's settings comes as an error event in a
		// 200 stream (gpt-6-sol and effort none).
		if msg := streamError(raw); msg != "" {
			r.Error = &msg
			return r
		}
	}
	r.OK = true
	return r
}

// streamError is the message of the first error, or failed response, in a
// Responses API event stream; "" when it has none.
func streamError(stream []byte) string {
	for line := range strings.Lines(string(stream)) {
		data, ok := strings.CutPrefix(strings.TrimSpace(line), "data:")
		if !ok {
			continue
		}
		var ev struct {
			Type  string `json:"type"`
			Error *struct {
				Message string `json:"message"`
			} `json:"error"`
			Response *struct {
				Error *struct {
					Message string `json:"message"`
				} `json:"error"`
			} `json:"response"`
		}
		if json.Unmarshal([]byte(strings.TrimSpace(data)), &ev) != nil {
			continue
		}
		switch {
		case ev.Type == "error" && ev.Error != nil:
			return ev.Error.Message
		case ev.Type == "error":
			return strings.TrimSpace(data)
		case ev.Type == "response.failed" && ev.Response != nil && ev.Response.Error != nil:
			return ev.Response.Error.Message
		case ev.Type == "response.failed":
			return "the response failed"
		}
	}
	return ""
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
