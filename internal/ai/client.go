// Package ai calls the models Outpost hands every service it supervises
// (outpost-service-spec.md v0.9.4, §15 "Using the operator's AI models").
//
// The shape of everything here follows from that section:
//
//   - The endpoint and token come from OUTPOST_AI_URL and OUTPOST_AI_TOKEN, and
//     when either is unset AI is simply off. A developer's machine, an older
//     Outpost, or shelf run by hand all land there, and none of them is an
//     error.
//   - The model sent is always "llm" or "system1". Outpost replaces it with
//     whatever the operator chose, and can change that choice while shelf runs,
//     so a provider model name here would be both wrong and ignored.
//   - Only the OpenAI Chat Completions API is spoken, because it is the one
//     every provider behind Outpost accepts.
//   - The HTTP timeout is ten minutes. Ollama loads a model on first use, and
//     Outpost itself waits that long for a provider to start answering.
//   - AI is an enhancement. Every failure is an *APIError a caller can sort
//     into "do without for now" (403, 502, 503), "wait" (429) or "this request
//     was bad" (400), and nothing in this package exits or panics on one.
//   - Answers cost the operator money, so the Store that keeps them is not a
//     cache: it is never thrown away and nothing is asked twice.
package ai

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"strconv"
	"strings"
	"time"
)

// Environment variables Outpost starts every service with (spec §3, §15).
const (
	EnvURL   = "OUTPOST_AI_URL"
	EnvToken = "OUTPOST_AI_TOKEN"
)

// Model names. The path decides the model, not these strings; they are sent
// so the request reads correctly (spec §15, "The path decides the model").
const (
	ModelLLM     = "llm"
	ModelSystem1 = "system1"
)

// requestTimeout covers a cold Ollama model load (spec §15, "Allow time").
const requestTimeout = 10 * time.Minute

// maxResponseBytes bounds what is read back. Replies here are small; this is
// only a guard against a misbehaving provider.
const maxResponseBytes = 8 << 20

// Client talks to Outpost's AI endpoint.
type Client struct {
	// HTTP is the transport. Its timeout is generous on purpose.
	HTTP *http.Client

	baseURL string
	// token is never logged, never put in an error, and never sent anywhere
	// but baseURL (spec §15, "What you're given").
	token string
}

// FromEnv builds a client from Outpost's environment, or returns nil when AI
// is not on offer.
func FromEnv() *Client {
	url, token := strings.TrimSpace(os.Getenv(EnvURL)), strings.TrimSpace(os.Getenv(EnvToken))
	if url == "" || token == "" {
		return nil
	}
	return New(url, token)
}

// New builds a client for an explicit endpoint.
func New(baseURL, token string) *Client {
	return &Client{
		HTTP:    &http.Client{Timeout: requestTimeout},
		baseURL: strings.TrimRight(baseURL, "/"),
		token:   token,
	}
}

// APIError is a non-2xx answer from Outpost or the provider behind it.
type APIError struct {
	Status     int
	Message    string
	RetryAfter time.Duration // from Retry-After, on 429 and some 503s
}

func (e *APIError) Error() string {
	if e.Message == "" {
		return fmt.Sprintf("ai: %d %s", e.Status, http.StatusText(e.Status))
	}
	return fmt.Sprintf("ai: %d %s", e.Status, e.Message)
}

func statusOf(err error) int {
	var e *APIError
	if errors.As(err, &e) {
		return e.Status
	}
	return 0
}

// Unavailable reports that no model is usable right now: none chosen, the
// provider unreachable, or no key for it. Carry on without and try later.
func Unavailable(err error) bool {
	s := statusOf(err)
	return s == http.StatusServiceUnavailable || s == http.StatusBadGateway
}

// Forbidden reports that the operator has not given this service that model.
func Forbidden(err error) bool { return statusOf(err) == http.StatusForbidden }

// Unauthorized reports a missing or wrong token. Not something to retry.
func Unauthorized(err error) bool { return statusOf(err) == http.StatusUnauthorized }

// RateLimited reports the provider's rate limit, passed through with its
// Retry-After.
func RateLimited(err error) bool { return statusOf(err) == http.StatusTooManyRequests }

// post sends one JSON request and decodes a 2xx reply into out.
func (c *Client) post(ctx context.Context, path string, body, out any) error {
	payload, err := json.Marshal(body)
	if err != nil {
		return fmt.Errorf("ai: encode request: %w", err)
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.baseURL+path, bytes.NewReader(payload))
	if err != nil {
		return fmt.Errorf("ai: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json")
	req.Header.Set("Authorization", "Bearer "+c.token)

	hc := c.HTTP
	if hc == nil {
		hc = &http.Client{Timeout: requestTimeout}
	}
	resp, err := hc.Do(req)
	if err != nil {
		// net/http errors carry the URL, which is loopback and harmless; the
		// token is in a header and never appears in them.
		return fmt.Errorf("ai: %w", err)
	}
	defer resp.Body.Close()

	data, err := io.ReadAll(io.LimitReader(resp.Body, maxResponseBytes))
	if err != nil {
		return fmt.Errorf("ai: read reply: %w", err)
	}
	if resp.StatusCode < 200 || resp.StatusCode > 299 {
		return &APIError{
			Status:     resp.StatusCode,
			Message:    c.scrub(errorMessage(data)),
			RetryAfter: retryAfter(resp.Header.Get("Retry-After")),
		}
	}
	if err := json.Unmarshal(data, out); err != nil {
		return fmt.Errorf("ai: decode reply: %w", err)
	}
	return nil
}

// scrub removes the token from text that will end up in a log line, in case a
// provider ever echoes the request back.
func (c *Client) scrub(s string) string {
	if c.token == "" {
		return s
	}
	return strings.ReplaceAll(s, c.token, "[redacted]")
}

// errorMessage pulls a message out of the shapes an error body comes in:
// Outpost's own {"type":"error","error":{"message":…}}, OpenAI's
// {"error":{"message":…}}, Ollama's {"error":"…"}, or plain text.
func errorMessage(body []byte) string {
	var v struct {
		Error   json.RawMessage `json:"error"`
		Message string          `json:"message"`
	}
	if json.Unmarshal(body, &v) == nil {
		var nested struct {
			Message string `json:"message"`
		}
		if json.Unmarshal(v.Error, &nested) == nil && nested.Message != "" {
			return nested.Message
		}
		var flat string
		if json.Unmarshal(v.Error, &flat) == nil && flat != "" {
			return flat
		}
		if v.Message != "" {
			return v.Message
		}
	}
	s := strings.TrimSpace(string(body))
	if len(s) > 300 {
		s = s[:300]
	}
	return s
}

// retryAfter reads Retry-After as seconds or an HTTP date.
func retryAfter(h string) time.Duration {
	h = strings.TrimSpace(h)
	if h == "" {
		return 0
	}
	if n, err := strconv.Atoi(h); err == nil && n >= 0 {
		return time.Duration(n) * time.Second
	}
	if t, err := http.ParseTime(h); err == nil {
		if d := time.Until(t); d > 0 {
			return d
		}
	}
	return 0
}

// --- LLM ---

type chatMessage struct {
	Role    string `json:"role"`
	Content string `json:"content"`
}

type chatRequest struct {
	Model     string        `json:"model"`
	Messages  []chatMessage `json:"messages"`
	MaxTokens int           `json:"max_tokens,omitempty"`
}

type chatResponse struct {
	Model   string `json:"model"`
	Choices []struct {
		Message struct {
			Content string `json:"content"`
		} `json:"message"`
	} `json:"choices"`
}

// Chat asks the operator's LLM one question and returns its reply and the
// model that answered, which is worth logging since it can change between
// requests.
func (c *Client) Chat(ctx context.Context, system, user string, maxTokens int) (text, model string, err error) {
	req := chatRequest{Model: ModelLLM, MaxTokens: maxTokens}
	if system != "" {
		req.Messages = append(req.Messages, chatMessage{Role: "system", Content: system})
	}
	req.Messages = append(req.Messages, chatMessage{Role: "user", Content: user})

	var resp chatResponse
	if err := c.post(ctx, "/openai/v1/chat/completions", req, &resp); err != nil {
		return "", "", err
	}
	if len(resp.Choices) == 0 {
		return "", resp.Model, errors.New("ai: reply has no choices")
	}
	return strings.TrimSpace(resp.Choices[0].Message.Content), resp.Model, nil
}

// --- System 1 ---

// Question is one typed question for the System 1 model.
//
// Criteria is a map[string]string of label to description for "choice", and
// a []string of levels, lowest first, for "score"; "noul" takes none. To stay
// portable across providers (spec §15), give every question Instructions, a
// choice at least two labels and a score two to ten levels.
type Question struct {
	Type         string `json:"type"`
	Instructions string `json:"instructions"`
	Criteria     any    `json:"criteria,omitempty"`
}

// Answer is one answer. A "refusal" carries nothing else; treat it as
// "don't know".
type Answer struct {
	Type          string             `json:"type"`
	Noul          float64            `json:"noul"`
	Choice        string             `json:"choice"`
	Score         float64            `json:"score"`
	Confidence    float64            `json:"confidence"`
	Probabilities map[string]float64 `json:"probabilities"`
}

type system1Request struct {
	Model     string              `json:"model"`
	State     any                 `json:"state"`
	Questions map[string]Question `json:"questions"`
}

type system1Response struct {
	Model   string            `json:"model"`
	Answers map[string]Answer `json:"answers"`
}

// System1 puts every question about one input to the System 1 model in a
// single request, which is how it is cheapest: they are answered in one pass.
func (c *Client) System1(ctx context.Context, state any, questions map[string]Question) (map[string]Answer, string, error) {
	var resp system1Response
	if err := c.post(ctx, "/systemone/v1/systemone",
		system1Request{Model: ModelSystem1, State: state, Questions: questions}, &resp); err != nil {
		return nil, "", err
	}
	return resp.Answers, resp.Model, nil
}
