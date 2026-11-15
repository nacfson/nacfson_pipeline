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
	"regexp"
	"strings"
	"sync/atomic"
	"time"

	"nacfson_pipeline/services/error-pipeline/internal/classify"
)

const Query = `{project=~"proj-.*"} OR {k8s.namespace.name=~"proj-.*"}`

const (
	maxLineBytes = 1 << 20

	tailOffset     = "5s"
	requestTimeout = 30 * time.Second
	errBodyLimit   = 512
)

type Client struct {
	BaseURL string
	HTTP    *http.Client

	skipped atomic.Int64
}

func (c *Client) Skipped() int64 { return c.skipped.Load() }

func (c *Client) httpClient() *http.Client {
	if c.HTTP != nil {
		return c.HTTP
	}
	return http.DefaultClient
}

func (c *Client) Tail(ctx context.Context, fn func(classify.Record) error) error {
	q := url.Values{"query": {Query}, "offset": {tailOffset}}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet,
		c.BaseURL+"/select/logsql/tail?"+q.Encode(), nil)
	if err != nil {
		return err
	}
	return c.stream(ctx, req, fn)
}

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
		if err != nil {
			return nil
		}
	}
}

func readLine(br *bufio.Reader) (line []byte, tooLong bool, err error) {
	var buf []byte
	for {
		chunk, rerr := br.ReadSlice('\n')
		if !tooLong {
			if len(buf)+len(chunk) > maxLineBytes+1 {
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
	Time     string `json:"_time"`
	StreamID string `json:"_stream_id"`

	Project   string `json:"project"`
	Pod       string `json:"pod"`
	Container string `json:"container"`
	Node      string `json:"node"`
	Message   string `json:"message"`

	K8sNamespace string `json:"k8s.namespace.name"`
	K8sPod       string `json:"k8s.pod.name"`
	K8sContainer string `json:"k8s.container.name"`
	K8sNode      string `json:"k8s.node.name"`
	MsgBody      string `json:"_msg"`

	Level            string `json:"level"`
	Msg              string `json:"msg"`
	EventName        string `json:"event.name"`
	ServiceVersion   string `json:"service.version"`
	TraceID          string `json:"trace_id"`
	ExceptionType    string `json:"exception.type"`
	ExceptionMessage string `json:"exception.message"`
	Stacktrace       string `json:"exception.stacktrace"`
	Fingerprint      string `json:"error.fingerprint"`
	TimeField        string `json:"time"`
}

type QuarantineRecord struct {
	Time         time.Time
	StreamID     string
	Project      string
	Pod          string
	Container    string
	Node         string
	Message      string
	IOStream     string
	ObservedTime string
}

func (c *Client) QueryQuarantine(ctx context.Context, from, to time.Time, fn func(QuarantineRecord) error) error {
	ctx, cancel := context.WithTimeout(ctx, requestTimeout)
	defer cancel()
	form := url.Values{
		"query": {"* | sort by (_time)"},
		"start": {from.UTC().Format(time.RFC3339Nano)},
		"end":   {to.UTC().Format(time.RFC3339Nano)},
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost,
		c.BaseURL+"/select/logsql/query", strings.NewReader(form.Encode()))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("AccountID", "0")
	req.Header.Set("ProjectID", "1")
	return c.streamQuarantine(ctx, req, fn)
}

func (c *Client) TailQuarantine(ctx context.Context, fn func(QuarantineRecord) error) error {
	q := url.Values{"query": {"*"}, "offset": {tailOffset}}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet,
		c.BaseURL+"/select/logsql/tail?"+q.Encode(), nil)
	if err != nil {
		return err
	}
	req.Header.Set("AccountID", "0")
	req.Header.Set("ProjectID", "1")
	return c.streamQuarantine(ctx, req, fn)
}

func (c *Client) streamQuarantine(ctx context.Context, req *http.Request, fn func(QuarantineRecord) error) error {
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
			if ctx.Err() != nil {
				return ctx.Err()
			}
			return err
		}
		if tooLong {
			c.skipped.Add(1)
		} else if len(line) > 0 {
			if rec, ok := c.decodeQuarantine(line); ok {
				if ferr := fn(rec); ferr != nil {
					return ferr
				}
			}
		}
		if ctx.Err() != nil {
			return ctx.Err()
		}
		if err != nil {
			return nil
		}
	}
}

type wireQuarantine struct {
	Time         string `json:"_time"`
	StreamID     string `json:"_stream_id"`
	Msg          string `json:"msg"`
	MsgBody      string `json:"_msg"`
	K8sNamespace string `json:"k8s.namespace.name"`
	K8sPod       string `json:"k8s.pod.name"`
	K8sContainer string `json:"k8s.container.name"`
	K8sNode      string `json:"k8s.node.name"`
	IOStream     string `json:"log.iostream"`
	ObservedTime string `json:"observed_time"`
}

func (c *Client) decodeQuarantine(line []byte) (QuarantineRecord, bool) {
	var w wireQuarantine
	if err := json.Unmarshal(line, &w); err != nil {
		c.skipped.Add(1)
		return QuarantineRecord{}, false
	}
	t, err := time.Parse(time.RFC3339Nano, w.Time)
	if err != nil {
		c.skipped.Add(1)
		return QuarantineRecord{}, false
	}
	msg := w.Msg
	if msg == "" {
		msg = w.MsgBody
	}
	return QuarantineRecord{
		Time:         t,
		StreamID:     w.StreamID,
		Project:      w.K8sNamespace,
		Pod:          w.K8sPod,
		Container:    w.K8sContainer,
		Node:         w.K8sNode,
		Message:      msg,
		IOStream:     w.IOStream,
		ObservedTime: w.ObservedTime,
	}, true
}

var dynamicKeyRe = regexp.MustCompile(`\d{4,}|[0-9a-fA-F]{32}`)

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

	var rawMap map[string]any
	var dynamicKey bool
	if err := json.Unmarshal(line, &rawMap); err == nil {
		for k := range rawMap {
			if dynamicKeyRe.MatchString(k) {
				dynamicKey = true
				break
			}
		}
	}

	project := w.Project
	if project == "" {
		project = w.K8sNamespace
	}
	pod := w.Pod
	if pod == "" {
		pod = w.K8sPod
	}
	container := w.Container
	if container == "" {
		container = w.K8sContainer
	}
	node := w.Node
	if node == "" {
		node = w.K8sNode
	}
	message := w.Message
	if message == "" {
		message = w.MsgBody
	}

	parsed := w.Level != ""
	timeRejected := parsed && w.TimeField != ""

	return classify.Record{
		Time:             t,
		StreamID:         w.StreamID,
		Project:          project,
		Pod:              pod,
		Container:        container,
		Node:             node,
		Message:          message,
		Parsed:           parsed,
		Level:            w.Level,
		Msg:              w.Msg,
		EventName:        w.EventName,
		ServiceVersion:   w.ServiceVersion,
		TraceID:          w.TraceID,
		ExceptionType:    w.ExceptionType,
		ExceptionMessage: w.ExceptionMessage,
		Stacktrace:       w.Stacktrace,
		Fingerprint:      w.Fingerprint,
		TimeRejected:     timeRejected,
		DynamicKey:       dynamicKey,
	}, true
}
