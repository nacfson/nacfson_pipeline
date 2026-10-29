package main

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"mime"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

func TestGoldenNotificationValidates(t *testing.T) {
	b, err := os.ReadFile("../../../error-pipeline/internal/notify/testdata/notification.json")
	if err != nil {
		t.Fatal(err)
	}
	var n Notification
	if err := json.Unmarshal(b, &n); err != nil {
		t.Fatal(err)
	}
	if err := n.Validate(); err != nil {
		t.Fatal(err)
	}
}

func TestValidateLimits(t *testing.T) {
	ok := Notification{Title: "t", Severity: "info"}
	cases := map[string]func(n *Notification){
		"empty title":    func(n *Notification) { n.Title = "" },
		"long title":     func(n *Notification) { n.Title = strings.Repeat("a", 257) },
		"bad severity":   func(n *Notification) { n.Severity = "warn" },
		"too many":       func(n *Notification) { n.Fields = make([]Field, 26) },
		"long name":      func(n *Notification) { n.Fields = []Field{{Name: strings.Repeat("a", 257), Value: "v"}} },
		"long value":     func(n *Notification) { n.Fields = []Field{{Name: "n", Value: strings.Repeat("a", 1025)}} },
		"big body":       func(n *Notification) { n.Body = strings.Repeat("a", 1<<20+1) },
		"big attachment": func(n *Notification) { n.Attachment = &Attachment{Name: "a", Content: strings.Repeat("a", 1<<20+1)} },
	}
	if err := ok.Validate(); err != nil {
		t.Fatal(err)
	}
	for name, mutate := range cases {
		n := ok
		mutate(&n)
		if n.Validate() == nil {
			t.Errorf("%s: expected validation error", name)
		}
	}
}

func parseMultipart(t *testing.T, p []byte, ct string) map[string]string {
	t.Helper()
	_, params, err := mime.ParseMediaType(ct)
	if err != nil {
		t.Fatal(err)
	}
	r := multipart.NewReader(bytes.NewReader(p), params["boundary"])
	out := map[string]string{}
	for {
		part, err := r.NextPart()
		if err == io.EOF {
			return out
		}
		if err != nil {
			t.Fatal(err)
		}
		b, _ := io.ReadAll(part)
		out[part.FormName()+"|"+part.FileName()] = string(b)
	}
}

func TestRenderSimpleEmbed(t *testing.T) {
	n := Notification{Title: "t", Severity: "fatal", Body: "hello", Fields: []Field{{Name: "a", Value: "b"}}}
	p, ct, err := RenderDiscord(n)
	if err != nil || ct != "application/json" {
		t.Fatal(ct, err)
	}
	var got struct {
		Embeds []struct {
			Title       string `json:"title"`
			Description string `json:"description"`
			Color       int    `json:"color"`
			Fields      []struct {
				Name, Value string
				Inline      bool
			} `json:"fields"`
		} `json:"embeds"`
	}
	if err := json.Unmarshal(p, &got); err != nil {
		t.Fatal(err)
	}
	e := got.Embeds[0]
	if len(got.Embeds) != 1 || e.Title != "t" || e.Description != "hello" || e.Color != 0x8E44AD ||
		len(e.Fields) != 1 || e.Fields[0].Name != "a" || e.Fields[0].Inline {
		t.Fatalf("%+v", got)
	}
}

func TestLongBodyBecomesAttachment(t *testing.T) {
	n := Notification{Title: "t", Severity: "error", Body: strings.Repeat("x", 5000)}
	p, ct, err := RenderDiscord(n)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(ct, "multipart/form-data") || !bytes.Contains(p, []byte(`filename="body.md"`)) {
		t.Fatal(ct)
	}
	parts := parseMultipart(t, p, ct)
	if parts["files[0]|body.md"] != n.Body {
		t.Fatal("body.md must hold full body")
	}
	var pl struct {
		Embeds []struct {
			Description string `json:"description"`
			Color       int    `json:"color"`
		} `json:"embeds"`
	}
	if err := json.Unmarshal([]byte(parts["payload_json|"]), &pl); err != nil {
		t.Fatal(err)
	}
	want := strings.Repeat("x", 3900) + "\n… (full text attached)"
	if pl.Embeds[0].Description != want || pl.Embeds[0].Color != 0xE74C3C {
		t.Fatalf("description %d chars", len(pl.Embeds[0].Description))
	}
}

func TestBodyAtLimitStaysInline(t *testing.T) {
	n := Notification{Title: "t", Severity: "info", Body: strings.Repeat("x", 4000)}
	_, ct, _ := RenderDiscord(n)
	if ct != "application/json" {
		t.Fatal(ct)
	}
}

func TestExplicitAttachmentAndLongBodySendBothFiles(t *testing.T) {
	n := Notification{Title: "t", Severity: "info", Body: strings.Repeat("y", 4001),
		Attachment: &Attachment{Name: "log.txt", Content: "logs"}}
	p, ct, err := RenderDiscord(n)
	if err != nil {
		t.Fatal(err)
	}
	parts := parseMultipart(t, p, ct)
	if parts["files[0]|body.md"] == "" || parts["files[1]|log.txt"] != "logs" {
		t.Fatalf("%v", parts)
	}
}

func TestExplicitAttachmentOnly(t *testing.T) {
	n := Notification{Title: "t", Severity: "info", Body: "b", Attachment: &Attachment{Name: "log.txt", Content: "logs"}}
	p, ct, _ := RenderDiscord(n)
	parts := parseMultipart(t, p, ct)
	if parts["files[0]|log.txt"] != "logs" {
		t.Fatalf("%v", parts)
	}
}

func fakeSleep(t *testing.T) *[]time.Duration {
	t.Helper()
	var got []time.Duration
	old := sleep
	sleep = func(d time.Duration) { got = append(got, d) }
	t.Cleanup(func() { sleep = old })
	return &got
}

func TestDiscord429RetriesAfter(t *testing.T) {
	slept := fakeSleep(t)
	var calls int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if atomic.AddInt32(&calls, 1) == 1 {
			w.WriteHeader(http.StatusTooManyRequests)
			_, _ = w.Write([]byte(`{"retry_after":0.01}`))
			return
		}
		w.WriteHeader(http.StatusNoContent)
	}))
	defer srv.Close()
	n := Notification{Title: "t", Severity: "info"}
	if err := SendDiscord(context.Background(), srv.Client(), srv.URL, n); err != nil {
		t.Fatal(err)
	}
	if calls != 2 || len(*slept) != 1 || (*slept)[0] != 10*time.Millisecond {
		t.Fatal(calls, *slept)
	}
}

func TestDiscord429CapsWaitAndGivesUp(t *testing.T) {
	slept := fakeSleep(t)
	var calls int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&calls, 1)
		w.WriteHeader(http.StatusTooManyRequests)
		_, _ = w.Write([]byte(`{"retry_after":9999}`))
	}))
	defer srv.Close()
	if err := SendDiscord(context.Background(), srv.Client(), srv.URL, Notification{Title: "t", Severity: "info"}); err == nil {
		t.Fatal("expected error")
	}
	if calls != 3 {
		t.Fatal(calls)
	}
	for _, d := range *slept {
		if d != 30*time.Second {
			t.Fatal(*slept)
		}
	}
}

func TestDiscord5xxGivesUpAfterThreeBackoffs(t *testing.T) {
	slept := fakeSleep(t)
	var calls int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&calls, 1)
		w.WriteHeader(http.StatusServiceUnavailable)
	}))
	defer srv.Close()
	if err := SendDiscord(context.Background(), srv.Client(), srv.URL, Notification{Title: "t", Severity: "info"}); err == nil {
		t.Fatal("expected error")
	}
	want := []time.Duration{time.Second, 2 * time.Second, 4 * time.Second}
	if calls != 4 || len(*slept) != 3 || (*slept)[0] != want[0] || (*slept)[1] != want[1] || (*slept)[2] != want[2] {
		t.Fatal(calls, *slept)
	}
}

func TestDiscord4xxFailsWithoutRetry(t *testing.T) {
	fakeSleep(t)
	var calls int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&calls, 1)
		w.WriteHeader(http.StatusBadRequest)
	}))
	defer srv.Close()
	if err := SendDiscord(context.Background(), srv.Client(), srv.URL, Notification{Title: "t", Severity: "info"}); err == nil || calls != 1 {
		t.Fatal(err, calls)
	}
}

func TestDiscordSendPostsContentType(t *testing.T) {
	var ct string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		ct = r.Header.Get("Content-Type")
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()
	n := Notification{Title: "t", Severity: "info", Attachment: &Attachment{Name: "a.txt", Content: "c"}}
	if err := SendDiscord(context.Background(), srv.Client(), srv.URL, n); err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(ct, "multipart/form-data; boundary=") {
		t.Fatal(ct)
	}
}

func TestDiscordTransportErrorOmitsWebhookURL(t *testing.T) {
	srv := httptest.NewServer(http.NotFoundHandler())
	url := srv.URL + "/api/webhooks/secret-token"
	srv.Close()
	err := SendDiscord(context.Background(), http.DefaultClient, url, Notification{Title: "t", Severity: "info"})
	if err == nil || strings.Contains(err.Error(), "secret-token") {
		t.Fatal(err)
	}
}
