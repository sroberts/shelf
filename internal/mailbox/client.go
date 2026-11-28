// Package mailbox turns mail sent to shelf's Outpost addresses into books.
//
// Outpost gives every service plus addresses on the operator's Gmail account
// and a loopback API to read what arrives there (outpost-service-spec.md §16,
// "Using email"). shelf's use for it is newsletters: each message becomes a
// small EPUB in the library, so it is indexed, shows in the OPDS catalog, and
// syncs to the reader like any other book.
//
// Mail is an enhancement. With the variables unset, or no Gmail account
// connected, shelf runs exactly as it would without it.
package mailbox

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"strconv"
	"strings"
	"time"
)

// Environment variables Outpost starts every service with (spec §3, §16).
const (
	EnvURL   = "OUTPOST_MAIL_URL"
	EnvToken = "OUTPOST_MAIL_TOKEN"
)

// Client talks to Outpost's mail endpoint.
type Client struct {
	base  string
	token string
	http  *http.Client
}

// FromEnv returns a client from the environment, or nil when either variable
// is unset: that is an Outpost without mail, or no Outpost at all.
func FromEnv() *Client {
	u, tok := strings.TrimSpace(os.Getenv(EnvURL)), strings.TrimSpace(os.Getenv(EnvToken))
	if u == "" || tok == "" {
		return nil
	}
	return New(u, tok)
}

// New builds a client. The token is a credential: it is sent only to baseURL
// and never logged.
func New(baseURL, token string) *Client {
	return &Client{
		base:  strings.TrimRight(baseURL, "/"),
		token: token,
		http:  &http.Client{Timeout: 30 * time.Second},
	}
}

// Me is GET /v1/me.
type Me struct {
	Ready     bool      `json:"ready"`
	Send      bool      `json:"send"`
	Addresses []Address `json:"addresses"`
}

// Address is one of shelf's addresses.
type Address struct {
	Route   string `json:"route"`
	Alias   bool   `json:"alias"`
	Note    string `json:"note,omitempty"`
	Address string `json:"address"`
	Feed    string `json:"feed,omitempty"`
}

// From is a message's sender.
type From struct {
	Name    string `json:"name"`
	Address string `json:"address"`
}

// Message is one received message.
type Message struct {
	ID          int64     `json:"id"`
	Route       string    `json:"route"`
	To          string    `json:"to"`
	From        From      `json:"from"`
	ReplyTo     string    `json:"reply_to"`
	Subject     string    `json:"subject"`
	MessageID   string    `json:"message_id"`
	Date        time.Time `json:"date"`
	Received    time.Time `json:"received"`
	Text        string    `json:"text"`
	HTML        string    `json:"html"`
	Attachments int       `json:"attachments"`
}

// APIError is a non-2xx answer, carrying the service's {"error": "..."}.
type APIError struct {
	Status  int
	Message string
}

func (e *APIError) Error() string {
	if e.Message == "" {
		return fmt.Sprintf("mail: %d %s", e.Status, http.StatusText(e.Status))
	}
	return fmt.Sprintf("mail: %d %s", e.Status, e.Message)
}

// Me reports shelf's addresses and whether a Gmail account is connected.
func (c *Client) Me(ctx context.Context) (Me, error) {
	var me Me
	err := c.do(ctx, http.MethodGet, "/v1/me", &me)
	return me, err
}

// Messages lists messages with an id above after, oldest first. next is the
// cursor to pass as after on the following call.
func (c *Client) Messages(ctx context.Context, after int64, limit int) ([]Message, int64, error) {
	q := url.Values{}
	q.Set("after", strconv.FormatInt(after, 10))
	if limit > 0 {
		q.Set("limit", strconv.Itoa(limit))
	}
	var page struct {
		Messages []Message `json:"messages"`
		Next     int64     `json:"next"`
	}
	if err := c.do(ctx, http.MethodGet, "/v1/messages?"+q.Encode(), &page); err != nil {
		return nil, after, err
	}
	next := page.Next
	if next < after {
		next = after
	}
	// Trust the ids we were given over a missing next.
	for _, m := range page.Messages {
		if m.ID > next {
			next = m.ID
		}
	}
	return page.Messages, next, nil
}

// Delete forgets a message once it has been dealt with.
func (c *Client) Delete(ctx context.Context, id int64) error {
	return c.do(ctx, http.MethodDelete, "/v1/messages/"+strconv.FormatInt(id, 10), nil)
}

func (c *Client) do(ctx context.Context, method, path string, out any) error {
	req, err := http.NewRequestWithContext(ctx, method, c.base+path, nil)
	if err != nil {
		return err
	}
	req.Header.Set("Authorization", "Bearer "+c.token)
	req.Header.Set("Accept", "application/json")

	resp, err := c.http.Do(req)
	if err != nil {
		// The URL is loopback and carries no secret; the token is in a header
		// that net/http errors never include.
		return fmt.Errorf("mail: %w", err)
	}
	defer resp.Body.Close()

	// Messages carry up to 2 MiB per part and a page up to 100 of them.
	body := io.LimitReader(resp.Body, 512<<20)
	if resp.StatusCode < 200 || resp.StatusCode > 299 {
		var e struct {
			Error string `json:"error"`
		}
		raw, _ := io.ReadAll(io.LimitReader(body, 64<<10))
		_ = json.Unmarshal(raw, &e)
		return &APIError{Status: resp.StatusCode, Message: e.Error}
	}
	if out == nil {
		_, _ = io.Copy(io.Discard, body)
		return nil
	}
	if err := json.NewDecoder(body).Decode(out); err != nil {
		return fmt.Errorf("mail: decode %s: %w", path, err)
	}
	return nil
}
