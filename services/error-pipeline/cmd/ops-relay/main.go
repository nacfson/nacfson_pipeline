package main

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/twmb/franz-go/pkg/kgo"

	"nacfson_pipeline/services/error-pipeline/internal/kafkaclient"
	"nacfson_pipeline/services/error-pipeline/internal/notify"
	"nacfson_pipeline/services/error-pipeline/internal/opsrelay"
)

const (
	eventsTopic  = "ops.platform.events"
	metricsTopic = "ops.platform.metrics"
)

func env(key, def string) string {
	if v := strings.TrimSpace(os.Getenv(key)); v != "" {
		return v
	}
	return def
}

func main() {
	slog.SetDefault(slog.New(slog.NewJSONHandler(os.Stderr, nil)))
	if len(os.Args) < 2 {
		fmt.Fprintln(os.Stderr, "usage: ops-relay serve | status [--json] | ready")
		os.Exit(2)
	}
	socket := env("STATUS_SOCKET", "/run/ops-relay/status.sock")
	switch os.Args[1] {
	case "status":
		os.Exit(status(socket, len(os.Args) > 2 && os.Args[2] == "--json"))
	case "ready":
		if _, err := os.Stat(env("READY_FILE", "/run/ops-relay/ready")); err != nil {
			fmt.Fprintln(os.Stderr, "not ready: state is still being rebuilt")
			os.Exit(1)
		}
	case "serve":
		if err := serve(socket); err != nil {
			slog.Error("ops-relay stopped", "err", err)
			os.Exit(1)
		}
	default:
		fmt.Fprintln(os.Stderr, "usage: ops-relay serve | status [--json] | ready")
		os.Exit(2)
	}
}

func status(socket string, asJSON bool) int {
	conn, err := net.DialTimeout("unix", socket, 5*time.Second)
	if err != nil {
		fmt.Fprintln(os.Stderr, "ops-relay is not serving:", err)
		return 1
	}
	defer conn.Close()
	mode := "text"
	if asJSON {
		mode = "json"
	}
	fmt.Fprintln(conn, mode)
	if _, err := io.Copy(os.Stdout, conn); err != nil {
		return 1
	}
	return 0
}

type outbox struct {
	mu    sync.Mutex
	ready *sync.Cond
	items []opsrelay.Message
}

func newOutbox() *outbox {
	o := &outbox{}
	o.ready = sync.NewCond(&o.mu)
	return o
}

func (o *outbox) push(msgs []opsrelay.Message) {
	if len(msgs) == 0 {
		return
	}
	o.mu.Lock()
	o.items = append(o.items, msgs...)
	o.mu.Unlock()
	o.ready.Signal()
}

func (o *outbox) head(ctx context.Context) (opsrelay.Message, bool) {
	o.mu.Lock()
	defer o.mu.Unlock()
	for len(o.items) == 0 && ctx.Err() == nil {
		o.ready.Wait()
	}
	if ctx.Err() != nil {
		return opsrelay.Message{}, false
	}
	return o.items[0], true
}

func (o *outbox) pop() {
	o.mu.Lock()
	o.items = o.items[1:]
	o.mu.Unlock()
}

type relay struct {
	engine *opsrelay.Engine
	board  *opsrelay.Board
	tasks  chan func()
	out    *outbox
	slots  *opsrelay.SlotHolders
	leases bool
}

func (r *relay) applySlots(changes []opsrelay.SlotChange, now time.Time) {
	for _, c := range changes {
		if c.Holds {
			r.board.SetSlot(c.Node, c.Slot)
			r.engine.NodeHoldsSlot(c.Node, c.Slot, c.Since, now)
			continue
		}
		r.board.ForgetSlot(c.Node)
		r.engine.NodeLeft(c.Node, now)
	}
}

func (r *relay) do(fn func()) { r.tasks <- fn }

func (r *relay) flush() { r.out.push(r.engine.Drain()) }

func decodeEvent(rec *kgo.Record) (opsrelay.Event, bool) {
	var ev opsrelay.Event
	if json.Unmarshal(rec.Value, &ev) != nil || ev.Schema != 1 || ev.Kind == "" || ev.Subject == "" {
		return ev, false
	}
	ev.Time = rec.Timestamp
	return ev, true
}

func decodeMetric(rec *kgo.Record) (opsrelay.Metric, bool) {
	var m opsrelay.Metric
	if json.Unmarshal(rec.Value, &m) != nil || m.Schema != 1 || m.Source == "" || m.Metric == "" {
		return m, false
	}
	m.Time = rec.Timestamp
	return m, true
}

func serve(socket string) error {
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGTERM, syscall.SIGINT)
	defer stop()
	nodes, err := opsrelay.ParseNodes(env("OPS_NODES", "1=a1-1:broker+controller:1024,2=a1-2:broker+controller:1024,3=micro-1:broker:384,4=micro-2:controller:256"))
	if err != nil {
		return err
	}
	tlsDir := env("KAFKA_TLS_DIR", "/etc/kafka-tls")
	kc := kafkaclient.Config{
		Brokers:  strings.Split(env("KAFKA_BROKERS", "10.200.0.11:9092,10.200.0.12:9092,10.200.0.13:9092"), ","),
		CAFile:   filepath.Join(tlsDir, "ca.crt"),
		CertFile: filepath.Join(tlsDir, "tls.crt"),
		KeyFile:  filepath.Join(tlsDir, "tls.key"),
	}
	board := opsrelay.NewBoard(nodes)
	r := &relay{
		engine: opsrelay.NewEngine(board.VMs(), board.Brokers(), time.Now()),
		board:  board,
		tasks:  make(chan func(), 4096),
		out:    newOutbox(),
		slots:  opsrelay.NewSlotHolders(),
	}
	go func() {
		<-ctx.Done()
		r.out.mu.Lock()
		r.out.ready.Broadcast()
		r.out.mu.Unlock()
	}()
	var relayWG sync.WaitGroup
	notifyURL := env("NOTIFY_URL", "http://notify-gateway.vault.svc:8080")
	go sender(ctx, &notify.Client{BaseURL: notifyURL}, env("NOTIFY_CHANNEL", "kafka"), r.out)
	if alertsTopic := strings.TrimSpace(os.Getenv("ALERTS_ERRORS_TOPIC")); alertsTopic != "" {
		relayWG.Add(1)
		go func() {
			defer relayWG.Done()
			opsrelay.RunErrorsRelayLoop(ctx, kc, opsrelay.ErrorsRelayConfig{
				Topic:   alertsTopic,
				Group:   env("ALERTS_ERRORS_GROUP", opsrelay.DefaultErrorsGroup),
				Channel: env("ALERTS_ERRORS_CHANNEL", opsrelay.DefaultErrorsChannel),
			}, &notify.Client{BaseURL: notifyURL})
		}()
	}
	go statusServer(ctx, socket, r)
	go rebuildThenConsume(ctx, kc, r, env("READY_FILE", "/run/ops-relay/ready"))
	go clusterLoop(ctx, kc, r)
	if kube, err := opsrelay.InCluster(env("KUBE_NAMESPACE", "oci-link")); err == nil {
		go followKube(ctx, kube, r)
	} else {
		slog.Warn("no Kubernetes watch", "err", err)
	}
	tick := time.NewTicker(5 * time.Second)
	defer tick.Stop()
	for {
		select {
		case <-ctx.Done():
			relayWG.Wait()
			return nil
		case fn := <-r.tasks:
			fn()
			r.flush()
		case now := <-tick.C:
			if r.leases {
				r.applySlots(r.slots.Sync(now), now)
			}
			r.engine.Tick(now)
			r.flush()
		}
	}
}

func sender(ctx context.Context, client *notify.Client, channel string, out *outbox) {
	for {
		m, ok := out.head(ctx)
		if !ok {
			return
		}
		severity := notify.SeverityInfo
		if m.Level == opsrelay.Down {
			severity = notify.SeverityError
		}
		n := notify.Notification{Title: m.String(), Severity: severity, Body: m.String()}
		for attempt := 1; ; attempt++ {
			sctx, cancel := context.WithTimeout(ctx, 15*time.Second)
			err := client.Send(sctx, channel, n)
			cancel()
			if err == nil {
				slog.Info("sent", "message", m.String())
				break
			}
			var status *notify.StatusError
			if errors.As(err, &status) && status.Permanent() {
				slog.Error("notify rejected the message, dropping it", "err", err, "message", m.String())
				break
			}
			slog.Warn("notify failed, will retry", "err", err, "attempt", attempt)
			sleep(ctx, min(time.Duration(attempt)*5*time.Second, time.Minute))
			if ctx.Err() != nil {
				return
			}
		}
		out.pop()
	}
}

func rebuildThenConsume(ctx context.Context, kc kafkaclient.Config, r *relay, readyFile string) {
	var next map[string]map[int32]int64
	for ctx.Err() == nil {
		var err error
		if next, err = rebuild(ctx, kc, r); err != nil {
			slog.Warn("rebuild failed, retrying", "err", err)
			r.do(func() { r.engine.Disconnected(time.Now()) })
			sleep(ctx, 30*time.Second)
			continue
		}
		break
	}
	if err := os.WriteFile(readyFile, []byte("ready\n"), 0o644); err != nil {
		slog.Error("readiness file", "err", err)
	}
	for ctx.Err() == nil {
		if err := consume(ctx, kc, r, next); err != nil && ctx.Err() == nil {
			slog.Warn("consumer stopped, restarting", "err", err)
			sleep(ctx, 10*time.Second)
		}
	}
}

func rebuild(ctx context.Context, kc kafkaclient.Config, r *relay) (map[string]map[int32]int64, error) {
	rctx, cancel := context.WithTimeout(ctx, 3*time.Minute)
	defer cancel()
	now := time.Now()
	evRecs, evEnds, err := kafkaclient.ReadSince(rctx, kc, eventsTopic, now.Add(-opsrelay.RebuildEvents))
	if err != nil {
		return nil, err
	}
	mRecs, mEnds, err := kafkaclient.ReadSince(rctx, kc, metricsTopic, now.Add(-opsrelay.RebuildMetrics))
	if err != nil {
		return nil, err
	}
	var events []opsrelay.Event
	var metrics []opsrelay.Metric
	for _, rec := range evRecs {
		if ev, ok := decodeEvent(rec); ok {
			events = append(events, ev)
		}
	}
	for _, rec := range mRecs {
		if m, ok := decodeMetric(rec); ok {
			metrics = append(metrics, m)
		}
	}
	done := make(chan struct{})
	r.do(func() {
		r.engine.Rebuild(events, metrics)
		for _, ev := range events {
			r.board.Event(ev)
		}
		for _, m := range metrics {
			r.board.Metric(m)
		}
		close(done)
	})
	<-done
	slog.Info("rebuilt", "events", len(events), "metrics", len(metrics))
	return map[string]map[int32]int64{eventsTopic: evEnds, metricsTopic: mEnds}, nil
}

func consume(ctx context.Context, kc kafkaclient.Config, r *relay, next map[string]map[int32]int64) error {
	cl, err := kafkaclient.NewPartitionReader(kc, next)
	if err != nil {
		return err
	}
	defer cl.Close()
	invalid := 0
	for ctx.Err() == nil {
		fetches := cl.PollFetches(ctx)
		if errs := fetches.Errors(); len(errs) > 0 && ctx.Err() == nil {
			return errs[0].Err
		}
		fetches.EachRecord(func(rec *kgo.Record) {
			next[rec.Topic][rec.Partition] = rec.Offset + 1
			switch rec.Topic {
			case eventsTopic:
				if ev, ok := decodeEvent(rec); ok {
					r.do(func() { r.engine.Event(ev); r.board.Event(ev) })
					return
				}
			case metricsTopic:
				if m, ok := decodeMetric(rec); ok {
					r.do(func() { r.engine.Metric(m); r.board.Metric(m) })
					return
				}
			}
			invalid++
			slog.Warn("invalid record dropped", "topic", rec.Topic, "dropped_total", invalid)
		})
	}
	return nil
}

func clusterLoop(ctx context.Context, kc kafkaclient.Config, r *relay) {
	admin, err := kafkaclient.NewAdmin(kc)
	if err != nil {
		slog.Error("admin client", "err", err)
		return
	}
	defer admin.Close()
	for ctx.Err() == nil {
		dctx, cancel := context.WithTimeout(ctx, 15*time.Second)
		facts, err := kafkaclient.Describe(dctx, admin)
		cancel()
		now := time.Now()
		if err != nil {
			r.do(func() { r.engine.Disconnected(now) })
		} else {
			r.do(func() {
				r.engine.Connected(now)
				controllers, leader := r.board.Controllers(now)
				r.board.SetCluster(opsrelay.Cluster{Brokers: facts.Brokers, Controllers: controllers, Leader: leader, UnderReplicated: facts.UnderReplicated, Offline: facts.Offline})
			})
		}
		sleep(ctx, 30*time.Second)
	}
}

func followKube(ctx context.Context, kube *opsrelay.Kube, r *relay) {
	handleEvent := func(raw json.RawMessage) {
		var ev opsrelay.KubeEvent
		if json.Unmarshal(raw, &ev) != nil {
			return
		}
		node := ev.Source.Host
		at := ev.Time()
		if at.IsZero() {
			at = time.Now()
		}
		r.engine.KubeEvent(ev.Reason, node, ev.Message, at)
	}
	handleLease := func(raw json.RawMessage) {
		var l opsrelay.Lease
		if json.Unmarshal(raw, &l) != nil {
			return
		}
		h, ok := opsrelay.LeaseHeld(l)
		if !ok {
			return
		}
		now := time.Now()
		r.applySlots(r.slots.Set(l.Metadata.Name, h, now), now)
		for _, m := range opsrelay.LeaseMetrics(l, now) {
			r.engine.Metric(m)
			r.board.Metric(m)
		}
	}
	leaseDeleted := func(raw json.RawMessage) {
		var l opsrelay.Lease
		if json.Unmarshal(raw, &l) != nil {
			return
		}
		now := time.Now()
		r.applySlots(r.slots.Remove(l.Metadata.Name, now), now)
	}
	leasesListed := func(items []json.RawMessage) {
		listed := map[string]opsrelay.HeldLease{}
		for _, raw := range items {
			var l opsrelay.Lease
			if json.Unmarshal(raw, &l) != nil {
				continue
			}
			if h, ok := opsrelay.LeaseHeld(l); ok {
				listed[l.Metadata.Name] = h
			}
		}
		now := time.Now()
		r.applySlots(r.slots.Replace(listed, now), now)
		r.leases = true
	}
	leasesLost := func() { r.leases = false }
	ignore := func(json.RawMessage) {}
	follow := func(group, resource string, handle, deleted func(json.RawMessage), order func([]json.RawMessage) []json.RawMessage, afterList func([]json.RawMessage), lost func()) {
		for ctx.Err() == nil {
			err := kube.Follow(ctx, group, resource,
				func(items []json.RawMessage) {
					items = order(items)
					r.do(func() {
						r.engine.Replay(func() {
							afterList(items)
							for _, it := range items {
								handle(it)
							}
						})
					})
				},
				func(obj json.RawMessage) { r.do(func() { handle(obj) }) },
				func(obj json.RawMessage) { r.do(func() { deleted(obj) }) })
			r.do(lost)
			if err != nil && !errors.Is(err, context.Canceled) {
				slog.Warn("kubernetes watch restarting", "resource", resource, "err", err)
			}
			sleep(ctx, 5*time.Second)
		}
	}
	asListed := func(items []json.RawMessage) []json.RawMessage { return items }
	go follow("", "events", handleEvent, ignore, opsrelay.SortEventsByTime, func([]json.RawMessage) {}, func() {})
	follow("coordination.k8s.io/v1", "leases", handleLease, leaseDeleted, asListed, leasesListed, leasesLost)
}

func statusServer(ctx context.Context, socket string, r *relay) {
	_ = os.Remove(socket)
	ln, err := net.Listen("unix", socket)
	if err != nil {
		slog.Error("status socket", "err", err)
		return
	}
	go func() { <-ctx.Done(); ln.Close() }()
	for {
		conn, err := ln.Accept()
		if err != nil {
			return
		}
		go func() {
			defer conn.Close()
			_ = conn.SetDeadline(time.Now().Add(10 * time.Second))
			mode, _ := bufio.NewReader(conn).ReadString('\n')
			out := make(chan []byte, 1)
			r.do(func() {
				v := r.board.View(time.Now(), r.engine)
				if strings.TrimSpace(mode) == "json" {
					b, _ := v.JSON()
					out <- append(b, '\n')
					return
				}
				out <- []byte(v.Text())
			})
			_, _ = conn.Write(<-out)
		}()
	}
}

func sleep(ctx context.Context, d time.Duration) {
	select {
	case <-ctx.Done():
	case <-time.After(d):
	}
}
