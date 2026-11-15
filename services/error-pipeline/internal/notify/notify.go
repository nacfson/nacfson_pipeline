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

const errBodyLimit = 512

const (
	SeverityError = "error"
	SeverityFatal = "fatal"
	SeverityInfo  = "info"
)

type Notification struct {
	Title      string      `json:"title"`
	Severity   string      `json:"severity"`
	Fields     []Field     `json:"fields"`
	Body       string      `json:"body"`
	Attachment *Attachment `json:"attachment,omitempty"`
}

type Field struct {
	Name  string `json:"name"`
	Value string `json:"value"`
}

type Attachment struct {
	Name    string `json:"name"`
	Content string `json:"content"`
}

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
