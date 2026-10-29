// Package notify posts destination-neutral notifications to the
// vault-notify-proxy, caps their burst rate and retries them from a bounded
// queue. Nothing here knows about Discord; the proxy owns the destination.
package notify

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
)

// errBodyLimit bounds the proxy error body echoed in an error message.
const errBodyLimit = 512

// Notification severity values the notify proxy accepts.
const (
	SeverityError = "error"
	SeverityFatal = "fatal"
	SeverityInfo  = "info"
)

// Notification is the destination-neutral message the notify proxy accepts.
// Its JSON form is the proxy's own contract
// (services/vault-proxies/cmd/notify-proxy/notification.go), so the tags are
// part of the API.
type Notification struct {
	Title      string      `json:"title"`
	Severity   string      `json:"severity"`
	Fields     []Field     `json:"fields"`
	Body       string      `json:"body"`
	Attachment *Attachment `json:"attachment,omitempty"`
}

// Field is one ordered key/value pair shown with a notification.
type Field struct {
	Name  string `json:"name"`
	Value string `json:"value"`
}

// Attachment carries a body too long for a message.
type Attachment struct {
	Name    string `json:"name"`
	Content string `json:"content"`
}

// Client posts notifications to the notify proxy. BaseURL is e.g.
// http://notify-gateway.vault.svc:8080. HTTP may be nil, in which case
// http.DefaultClient is used; the caller's context bounds each request.
type Client struct {
	BaseURL string
	HTTP    *http.Client
}

func (c *Client) httpClient() *http.Client {
	if c.HTTP != nil {
		return c.HTTP
	}
	return http.DefaultClient
}

// Send posts n to channel. The proxy answers 202 Accepted once it has taken
// the message; any other status is an error, so the caller can retry.
func (c *Client) Send(ctx context.Context, channel string, n Notification) error {
	body, err := json.Marshal(n)
	if err != nil {
		return fmt.Errorf("notify: encode %s: %w", channel, err)
	}
	endpoint := strings.TrimRight(c.BaseURL, "/") + "/v1/notify/" + url.PathEscape(channel)
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, bytes.NewReader(body))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := c.httpClient().Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusAccepted {
		b, _ := io.ReadAll(io.LimitReader(resp.Body, errBodyLimit))
		return fmt.Errorf("notify: channel %s: unexpected status %d: %s",
			channel, resp.StatusCode, strings.TrimSpace(string(b)))
	}
	return nil
}
