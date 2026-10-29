package vlogs

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"nacfson_pipeline/services/error-pipeline/internal/classify"
)

const line1 = `{"_time":"2026-10-29T09:00:00.123Z","_stream_id":"s1","project":"proj-pn","pod":"p","container":"c","node":"n","message":"{\"level\":\"error\"}"}`
const line2 = `{"_time":"2026-10-29T09:00:01Z","_stream_id":"s2","project":"proj-pn","pod":"p2","container":"c2","node":"n2","message":"second"}`

func collect(out *[]classify.Record) func(classify.Record) error {
	return func(r classify.Record) error {
		*out = append(*out, r)
		return nil
	}
}

func TestTailDecodesRecords(t *testing.T) {
	var gotQuery, gotOffset, gotMethod, gotPath string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotMethod, gotPath = r.Method, r.URL.Path
		gotQuery = r.URL.Query().Get("query")
		gotOffset = r.URL.Query().Get("offset")
		fmt.Fprintln(w, line1)
		fmt.Fprintln(w)
		fmt.Fprintln(w, line2)
	}))
	defer srv.Close()

	c := &Client{BaseURL: srv.URL}
	var got []classify.Record
	if err := c.Tail(context.Background(), collect(&got)); err != nil {
		t.Fatalf("Tail: %v", err)
	}
	if gotMethod != http.MethodGet || gotPath != "/select/logsql/tail" {
		t.Errorf("request = %s %s", gotMethod, gotPath)
	}
	if gotQuery != Query || gotOffset != "5s" {
		t.Errorf("query=%q offset=%q", gotQuery, gotOffset)
	}
	if len(got) != 2 {
		t.Fatalf("got %d records, want 2", len(got))
	}
	want := classify.Record{
		Time:      time.Date(2026, 10, 29, 9, 0, 0, 123000000, time.UTC),
		StreamID:  "s1",
		Project:   "proj-pn",
		Pod:       "p",
		Container: "c",
		Node:      "n",
		Message:   `{"level":"error"}`,
	}
	if !got[0].Time.Equal(want.Time) {
		t.Errorf("time = %v, want %v", got[0].Time, want.Time)
	}
	got[0].Time = want.Time
	if got[0] != want {
		t.Errorf("record = %+v, want %+v", got[0], want)
	}
	if got[1].Pod != "p2" || got[1].Message != "second" {
		t.Errorf("second record = %+v", got[1])
	}
}

func TestCatchUpSendsRangeAndSort(t *testing.T) {
	var form map[string]string
	var method, path, ctype string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		method, path = r.Method, r.URL.Path
		ctype = r.Header.Get("Content-Type")
		if err := r.ParseForm(); err != nil {
			t.Errorf("ParseForm: %v", err)
		}
		form = map[string]string{
			"query": r.PostForm.Get("query"),
			"start": r.PostForm.Get("start"),
			"end":   r.PostForm.Get("end"),
		}
		fmt.Fprintln(w, line1)
	}))
	defer srv.Close()

	loc := time.FixedZone("KST", 9*3600)
	from := time.Date(2026, 10, 29, 17, 0, 0, 5, loc) // 08:00:00.000000005Z
	to := time.Date(2026, 10, 29, 18, 0, 0, 0, loc)   // 09:00:00Z
	c := &Client{BaseURL: srv.URL}
	var got []classify.Record
	if err := c.CatchUp(context.Background(), from, to, collect(&got)); err != nil {
		t.Fatalf("CatchUp: %v", err)
	}
	if method != http.MethodPost || path != "/select/logsql/query" {
		t.Errorf("request = %s %s", method, path)
	}
	if !strings.HasPrefix(ctype, "application/x-www-form-urlencoded") {
		t.Errorf("content-type = %q", ctype)
	}
	if form["query"] != Query+" | sort by (_time)" {
		t.Errorf("query = %q", form["query"])
	}
	if form["start"] != "2026-10-29T08:00:00.000000005Z" || form["end"] != "2026-10-29T09:00:00Z" {
		t.Errorf("start=%q end=%q", form["start"], form["end"])
	}
	if len(got) != 1 {
		t.Errorf("got %d records, want 1", len(got))
	}
}

func TestTailStopsOnContextCancel(t *testing.T) {
	// The server sends one full line and half of a second, then goes
	// silent. Cancellation must unblock the stalled read.
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprintln(w, line1)
		fmt.Fprint(w, line2[:20]) // partial line, no newline
		w.(http.Flusher).Flush()
		<-r.Context().Done()
	}))
	defer srv.Close()

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	c := &Client{BaseURL: srv.URL}
	got := make(chan classify.Record, 1)
	done := make(chan error, 1)
	go func() {
		done <- c.Tail(ctx, func(r classify.Record) error {
			got <- r
			return nil
		})
	}()

	select {
	case <-got:
	case <-time.After(5 * time.Second):
		t.Fatal("first record not delivered")
	}
	time.Sleep(50 * time.Millisecond) // let Tail block reading the silent stream
	select {
	case err := <-done:
		t.Fatalf("Tail returned before cancel: %v", err)
	default:
	}
	cancel()
	select {
	case err := <-done:
		if err != context.Canceled {
			t.Fatalf("err = %v, want context.Canceled", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("Tail did not return after cancel")
	}
	if n := c.Skipped(); n != 0 {
		t.Errorf("Skipped = %d, want 0 (partial line at cancel is not malformed)", n)
	}
}

func TestMalformedLineSkippedNotFatal(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprintln(w, `not json`)
		fmt.Fprintln(w, `{"_time":"yesterday","message":"x"}`)
		fmt.Fprintln(w, line1)
	}))
	defer srv.Close()

	c := &Client{BaseURL: srv.URL}
	var got []classify.Record
	if err := c.Tail(context.Background(), collect(&got)); err != nil {
		t.Fatalf("Tail: %v", err)
	}
	if len(got) != 1 || got[0].StreamID != "s1" {
		t.Fatalf("got %+v", got)
	}
	if c.Skipped() != 2 {
		t.Errorf("Skipped = %d, want 2", c.Skipped())
	}
}

func TestOverlongLineSkippedAndReadingContinues(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprintln(w, line1)
		fmt.Fprintln(w, strings.Repeat("x", maxLineBytes+10))
		fmt.Fprintln(w, line2)
	}))
	defer srv.Close()

	c := &Client{BaseURL: srv.URL}
	var got []classify.Record
	if err := c.Tail(context.Background(), collect(&got)); err != nil {
		t.Fatalf("Tail: %v", err)
	}
	if len(got) != 2 || got[1].StreamID != "s2" {
		t.Fatalf("got %+v", got)
	}
	if c.Skipped() != 1 {
		t.Errorf("Skipped = %d, want 1", c.Skipped())
	}
}

func TestCallbackErrorStopsStream(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprintln(w, line1)
		fmt.Fprintln(w, line2)
	}))
	defer srv.Close()

	boom := fmt.Errorf("boom")
	n := 0
	c := &Client{BaseURL: srv.URL}
	err := c.Tail(context.Background(), func(classify.Record) error { n++; return boom })
	if err != boom || n != 1 {
		t.Fatalf("err=%v n=%d", err, n)
	}
}

func TestNon200ReturnsStatusError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "bad query", http.StatusBadRequest)
	}))
	defer srv.Close()

	c := &Client{BaseURL: srv.URL}
	err := c.Tail(context.Background(), func(classify.Record) error { return nil })
	if err == nil || !strings.Contains(err.Error(), "400") {
		t.Fatalf("err = %v", err)
	}
	err = c.CatchUp(context.Background(), time.Now().Add(-time.Hour), time.Now(), func(classify.Record) error { return nil })
	if err == nil || !strings.Contains(err.Error(), "400") {
		t.Fatalf("catchup err = %v", err)
	}
}

func TestReady(t *testing.T) {
	status := http.StatusOK
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/health" {
			t.Errorf("path = %s", r.URL.Path)
		}
		w.WriteHeader(status)
	}))
	defer srv.Close()

	c := &Client{BaseURL: srv.URL}
	if err := c.Ready(context.Background()); err != nil {
		t.Fatalf("Ready: %v", err)
	}
	status = http.StatusServiceUnavailable
	err := c.Ready(context.Background())
	if err == nil || !strings.Contains(err.Error(), "503") {
		t.Fatalf("err = %v", err)
	}
}
