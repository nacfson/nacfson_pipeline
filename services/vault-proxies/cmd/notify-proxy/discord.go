package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"mime/multipart"
	"net/http"
	"net/textproto"
	"net/url"
	"strings"
	"time"
)

const (
	colorError = 0xE74C3C
	colorFatal = 0x8E44AD
	colorInfo  = 0x3498DB

	maxInlineBody    = 4000 // longer bodies move to an attachment
	truncatedBodyLen = 3900
	truncationSuffix = "\n… (full text attached)"
	bodyFileName     = "body.md"

	maxRetryWait     = 30 * time.Second
	max429Tries      = 3
	max5xxRetries    = 3
	maxResponseBytes = 64 << 10
)

// sleep is replaced in tests so retry waits run instantly.
var sleep = time.Sleep

type discordEmbedField struct {
	Name   string `json:"name"`
	Value  string `json:"value"`
	Inline bool   `json:"inline"`
}

type discordEmbed struct {
	Title       string              `json:"title"`
	Description string              `json:"description,omitempty"`
	Color       int                 `json:"color"`
	Fields      []discordEmbedField `json:"fields,omitempty"`
}

type discordPayload struct {
	Embeds []discordEmbed `json:"embeds"`
}

func severityColor(s string) int {
	switch s {
	case "fatal":
		return colorFatal
	case "info":
		return colorInfo
	default:
		return colorError
	}
}

// RenderDiscord turns a Notification into a Discord webhook request body. It
// returns plain JSON, or multipart/form-data when files must be attached.
func RenderDiscord(n Notification) ([]byte, string, error) {
	embed := discordEmbed{Title: n.Title, Description: n.Body, Color: severityColor(n.Severity)}
	for _, f := range n.Fields {
		embed.Fields = append(embed.Fields, discordEmbedField{Name: f.Name, Value: f.Value})
	}

	type file struct{ name, content string }
	var files []file
	if runes := []rune(n.Body); len(runes) > maxInlineBody {
		embed.Description = string(runes[:truncatedBodyLen]) + truncationSuffix
		files = append(files, file{bodyFileName, n.Body})
	}
	if n.Attachment != nil {
		files = append(files, file{n.Attachment.Name, n.Attachment.Content})
	}

	payload, err := json.Marshal(discordPayload{Embeds: []discordEmbed{embed}})
	if err != nil {
		return nil, "", err
	}
	if len(files) == 0 {
		return payload, "application/json", nil
	}

	var buf bytes.Buffer
	mw := multipart.NewWriter(&buf)
	jh := textproto.MIMEHeader{}
	jh.Set("Content-Disposition", `form-data; name="payload_json"`)
	jh.Set("Content-Type", "application/json")
	jw, err := mw.CreatePart(jh)
	if err != nil {
		return nil, "", err
	}
	if _, err := jw.Write(payload); err != nil {
		return nil, "", err
	}
	for i, f := range files {
		fh := textproto.MIMEHeader{}
		fh.Set("Content-Disposition", fmt.Sprintf(`form-data; name="files[%d]"; filename="%s"`, i, escapeQuotes(f.name)))
		fh.Set("Content-Type", "application/octet-stream")
		fw, err := mw.CreatePart(fh)
		if err != nil {
			return nil, "", err
		}
		if _, err := io.WriteString(fw, f.content); err != nil {
			return nil, "", err
		}
	}
	if err := mw.Close(); err != nil {
		return nil, "", err
	}
	return buf.Bytes(), mw.FormDataContentType(), nil
}

var quoteEscaper = strings.NewReplacer(`\`, `\\`, `"`, `\"`, "\r", "", "\n", "")

func escapeQuotes(s string) string { return quoteEscaper.Replace(s) }

// SendDiscord posts n to the webhook. It retries 429 (waiting retry_after, at
// most 3 tries) and 5xx (1s, 2s, 4s backoff), and fails fast on other errors.
// Errors never contain the webhook URL, which is a secret.
func SendDiscord(ctx context.Context, client *http.Client, webhookURL string, n Notification) error {
	payload, contentType, err := RenderDiscord(n)
	if err != nil {
		return err
	}
	tries429, retries5xx := 0, 0
	for {
		status, retryAfter, err := postOnce(ctx, client, webhookURL, payload, contentType)
		if err != nil {
			return err
		}
		switch {
		case status >= 200 && status < 300:
			return nil
		case status == http.StatusTooManyRequests:
			tries429++
			if tries429 >= max429Tries {
				return fmt.Errorf("discord webhook rate limited after %d tries", tries429)
			}
			sleep(retryAfter)
		case status >= 500:
			retries5xx++
			if retries5xx > max5xxRetries {
				return fmt.Errorf("discord webhook returned status %d after %d retries", status, max5xxRetries)
			}
			sleep(time.Second << (retries5xx - 1))
		default:
			return fmt.Errorf("discord webhook returned status %d", status)
		}
		if err := ctx.Err(); err != nil {
			return err
		}
	}
}

func postOnce(ctx context.Context, client *http.Client, webhookURL string, payload []byte, contentType string) (int, time.Duration, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, webhookURL, bytes.NewReader(payload))
	if err != nil {
		return 0, 0, errors.New("invalid discord webhook request")
	}
	req.Header.Set("Content-Type", contentType)
	resp, err := client.Do(req)
	if err != nil {
		// *url.Error embeds the request URL; unwrap so the secret stays out of logs.
		var ue *url.Error
		if errors.As(err, &ue) {
			err = ue.Err
		}
		return 0, 0, fmt.Errorf("discord webhook request failed: %w", err)
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(io.LimitReader(resp.Body, maxResponseBytes))

	var wait time.Duration
	if resp.StatusCode == http.StatusTooManyRequests {
		var rl struct {
			RetryAfter float64 `json:"retry_after"`
		}
		_ = json.Unmarshal(body, &rl)
		wait = time.Duration(rl.RetryAfter * float64(time.Second))
		if wait <= 0 {
			wait = time.Second
		}
		if wait > maxRetryWait || rl.RetryAfter > maxRetryWait.Seconds() {
			wait = maxRetryWait
		}
	}
	return resp.StatusCode, wait, nil
}
