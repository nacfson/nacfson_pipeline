// Package vlogs reads project log entries from VictoriaLogs: a live tail for
// new entries and a ranged query for catching up after a restart.
package vlogs

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"sync/atomic"
	"time"

	"nacfson_pipeline/services/error-pipeline/internal/classify"
)

// Query selects every project log entry and the fields the detector needs.
const Query = "{project=~\"proj-.*\"} | fields _time, _stream_id, project, pod, container, node, message"

const (
	// maxLineBytes bounds one JSON line. Longer lines are discarded up to
	// their newline, counted in Skipped, and reading continues.
	maxLineBytes = 1 << 20

	tailOffset     = "5s"
	requestTimeout = 30 * time.Second
	errBodyLimit   = 512
)

// Client talks to a VictoriaLogs instance. BaseURL is e.g.
// http://victorialogs.observability.svc:9428. HTTP may be nil; a supplied
// client must not set Timeout, because Tail streams indefinitely.
type Client struct {
	BaseURL string
	HTTP    *http.Client

	skipped atomic.Int64
}

// Skipped returns how many malformed or over-long lines were dropped.
func (c *Client) Skipped() int64 { return c.skipped.Load() }

func (c *Client) httpClient() *http.Client {
	if c.HTTP != nil {
		return c.HTTP
	}
	return http.DefaultClient
}

// Tail streams new entries to fn until the server closes the stream (nil
// return, caller reconnects), ctx is cancelled (ctx.Err()), or fn fails.
func (c *Client) Tail(ctx context.Context, fn func(classify.Record) error) error {
	q := url.Values{"query": {Query}, "offset": {tailOffset}}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet,
		c.BaseURL+"/select/logsql/tail?"+q.Encode(), nil)
	if err != nil {
		return err
	}
	return c.stream(ctx, req, fn)
}

// CatchUp delivers entries in [from, to] in time order to fn.
func (c *Client) CatchUp(ctx context.Context, from, to time.Time, fn func(classify.Record) error) error {
	ctx, cancel := context.WithTimeout(ctx, requestTimeout)
	defer cancel()
	form := url.Values{
		"query": {Query + " | sort by (_time)"},
		"start": {from.UTC().Format(time.RFC3339Nano)},
		"end":   {to.UTC().Format(time.RFC3339Nano)},
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost,
		c.BaseURL+"/select/logsql/query", strings.NewReader(form.Encode()))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	return c.stream(ctx, req, fn)
}

// Ready reports whether VictoriaLogs answers its health endpoint with 200.
func (c *Client) Ready(ctx context.Context) error {
	ctx, cancel := context.WithTimeout(ctx, requestTimeout)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, c.BaseURL+"/health", nil)
	if err != nil {
		return err
	}
	resp, err := c.httpClient().Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return statusError(resp)
	}
	return nil
}

func statusError(resp *http.Response) error {
	body, _ := io.ReadAll(io.LimitReader(resp.Body, errBodyLimit))
	return fmt.Errorf("vlogs: unexpected status %d: %s", resp.StatusCode, strings.TrimSpace(string(body)))
}

func (c *Client) stream(ctx context.Context, req *http.Request, fn func(classify.Record) error) error {
	resp, err := c.httpClient().Do(req)
	if err != nil {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return statusError(resp)
	}

	br := bufio.NewReaderSize(resp.Body, 64<<10)
	for {
		line, tooLong, err := readLine(br)
		if err != nil && !errors.Is(err, io.EOF) {
			// A failed read (cancel, reset) may leave a partial line;
			// it is not a malformed record, so drop it uncounted.
			if ctx.Err() != nil {
				return ctx.Err()
			}
			return err
		}
		if tooLong {
			c.skipped.Add(1)
		} else if len(line) > 0 {
			if rec, ok := c.decode(line); ok {
				if ferr := fn(rec); ferr != nil {
					return ferr
				}
			}
		}
		if ctx.Err() != nil {
			return ctx.Err()
		}
		if err != nil { // io.EOF: server closed the stream
			return nil
		}
	}
}

// readLine returns the next line without its newline. A line longer than
// maxLineBytes is consumed through its newline and reported as tooLong with
// no content. The returned error is non-nil once the reader is exhausted;
// the final unterminated line, if any, is still returned alongside it.
func readLine(br *bufio.Reader) (line []byte, tooLong bool, err error) {
	var buf []byte
	for {
		chunk, rerr := br.ReadSlice('\n')
		if !tooLong {
			if len(buf)+len(chunk) > maxLineBytes+1 { // +1 for the newline
				tooLong, buf = true, nil
			} else {
				buf = append(buf, chunk...)
			}
		}
		if errors.Is(rerr, bufio.ErrBufferFull) {
			continue
		}
		if tooLong {
			return nil, true, rerr
		}
		return bytes.TrimSpace(buf), false, rerr
	}
}

type wireRecord struct {
	Time      string `json:"_time"`
	StreamID  string `json:"_stream_id"`
	Project   string `json:"project"`
	Pod       string `json:"pod"`
	Container string `json:"container"`
	Node      string `json:"node"`
	Message   string `json:"message"`
}

func (c *Client) decode(line []byte) (classify.Record, bool) {
	var w wireRecord
	if err := json.Unmarshal(line, &w); err != nil {
		c.skipped.Add(1)
		return classify.Record{}, false
	}
	t, err := time.Parse(time.RFC3339Nano, w.Time)
	if err != nil {
		c.skipped.Add(1)
		return classify.Record{}, false
	}
	return classify.Record{
		Time:      t,
		StreamID:  w.StreamID,
		Project:   w.Project,
		Pod:       w.Pod,
		Container: w.Container,
		Node:      w.Node,
		Message:   w.Message,
	}, true
}
