package slots

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"nacfson_pipeline/services/wg-slot-agent/internal/kube"
	"nacfson_pipeline/services/wg-slot-agent/internal/kubetest"
)

var t0 = time.Date(2026, 11, 18, 12, 0, 0, 0, time.UTC)

func held(name, holder string, renewed time.Time) kube.Lease {
	d := int32(90)
	return kube.Lease{
		Metadata: kube.ObjectMeta{Name: name},
		Spec:     kube.LeaseSpec{HolderIdentity: &holder, LeaseDurationSeconds: &d, RenewTime: formatTime(renewed)},
	}
}

func newClaimer(api *kubetest.FakeAPI, node string, now time.Time) *Claimer {
	return &Claimer{API: api.Client(), Node: node, Now: func() time.Time { return now }}
}

func TestClaimTakesLowestFreeSlot(t *testing.T) {
	api := kubetest.NewFakeAPI()
	defer api.Close()
	api.Nodes["node-a"] = true
	api.PutLease(held("wg-slot-0", "node-a", t0))

	slot, err := newClaimer(api, "node-b", t0).Claim(context.Background(), []int{0, 1, 2, 3})
	if err != nil || slot != 1 {
		t.Fatalf("Claim = %d, %v; want slot 1", slot, err)
	}
	if got := Holder(api.Lease("wg-slot-1")); got != "node-b" {
		t.Fatalf("wg-slot-1 holder = %q, want node-b", got)
	}
	if got := *api.Lease("wg-slot-1").Spec.LeaseDurationSeconds; got != 90 {
		t.Fatalf("lease duration = %d, want 90", got)
	}
}

func TestClaimTakesOverExpiredLease(t *testing.T) {
	api := kubetest.NewFakeAPI()
	defer api.Close()
	api.Nodes["node-a"] = true
	api.PutLease(held("wg-slot-0", "node-a", t0.Add(-91*time.Second)))

	slot, err := newClaimer(api, "node-b", t0).Claim(context.Background(), []int{0, 1})
	if err != nil || slot != 0 {
		t.Fatalf("Claim = %d, %v; want expired slot 0", slot, err)
	}
}

func TestClaimTakesOverLeaseOfGoneNode(t *testing.T) {
	api := kubetest.NewFakeAPI()
	defer api.Close()
	api.PutLease(held("wg-slot-0", "deleted-node", t0))

	slot, err := newClaimer(api, "node-b", t0).Claim(context.Background(), []int{0, 1})
	if err != nil || slot != 0 {
		t.Fatalf("Claim = %d, %v; want slot 0 of the deleted node", slot, err)
	}
}

func TestClaimOnConflictRelistsAndTakesNextSlot(t *testing.T) {
	api := kubetest.NewFakeAPI()
	defer api.Close()
	api.Nodes["node-a"] = true
	api.PutLease(held("wg-slot-0", "", t0))
	api.PutLease(held("wg-slot-1", "", t0))
	var once sync.Once
	api.BeforePut = func(name string) {
		if name == "wg-slot-0" {
			once.Do(func() { api.PutLease(held("wg-slot-0", "node-a", t0)) })
		}
	}

	slot, err := newClaimer(api, "node-b", t0).Claim(context.Background(), []int{0, 1})
	if err != nil || slot != 1 {
		t.Fatalf("Claim = %d, %v; want slot 1 after the conflict on slot 0", slot, err)
	}
	if got := Holder(api.Lease("wg-slot-0")); got != "node-a" {
		t.Fatalf("wg-slot-0 holder = %q, want node-a to keep it", got)
	}
}

func TestClaimOnlyListedSlots(t *testing.T) {
	api := kubetest.NewFakeAPI()
	defer api.Close()
	slot, err := newClaimer(api, "node-b", t0).Claim(context.Background(), []int{2})
	if err != nil || slot != 2 {
		t.Fatalf("Claim = %d, %v; want the only listed slot 2", slot, err)
	}
	if _, ok := api.Leases["wg-slot-0"]; ok {
		t.Fatal("an unlisted slot was claimed")
	}
}

type fakeTunnel struct {
	ups   []int
	downs int
}

func (f *fakeTunnel) Up(slot int) error { f.ups = append(f.ups, slot); return nil }
func (f *fakeTunnel) Down() error       { f.downs++; return nil }

type harness struct {
	api    *kubetest.FakeAPI
	tunnel *fakeTunnel
	agent  *Agent
	sleeps []time.Duration
	pool   []int
	obs    Observation
	ticks  int
	cancel context.CancelFunc
}

func newHarness(t *testing.T, node string, ticks int) (*harness, context.Context) {
	ctx, cancel := context.WithCancel(context.Background())
	h := &harness{api: kubetest.NewFakeAPI(), tunnel: &fakeTunnel{}, pool: []int{0, 1}, ticks: ticks, cancel: cancel}
	t.Cleanup(h.api.Close)
	h.api.Nodes[node] = true
	client := h.api.Client()
	h.agent = &Agent{
		Claimer: &Claimer{API: client, Node: node, Now: func() time.Time { return t0 }},
		Pool:    func() ([]int, error) { return h.pool, nil },
		Tunnel:  h.tunnel,
		Event: func(ctx context.Context, eventType, reason, message string) error {
			return client.CreateEvent(ctx, kube.ObjectReference{Kind: "Pod", Name: "wg-slot-agent-" + node, Namespace: "oci-link"}, eventType, reason, message, node, t0)
		},
		Observe: func() Observation { return h.obs },
		Sleep: func(ctx context.Context, d time.Duration) error {
			h.sleeps = append(h.sleeps, d)
			if len(h.sleeps) >= h.ticks {
				h.cancel()
				return errors.New("stopped")
			}
			return nil
		},
	}
	return h, ctx
}

func TestNoFreeSlotEmitsEventStartsNoTunnelAndRetries(t *testing.T) {
	h, ctx := newHarness(t, "node-c", 3)
	h.api.Nodes["node-a"] = true
	h.api.PutLease(held("wg-slot-0", "node-a", t0))
	h.api.PutLease(held("wg-slot-1", "node-a", t0))

	if err := h.agent.Run(ctx); err != nil {
		t.Fatal(err)
	}
	if len(h.tunnel.ups) != 0 {
		t.Fatalf("tunnel started for slots %v without a free slot", h.tunnel.ups)
	}
	reasons := h.api.EventReasons()
	if len(reasons) != 3 || reasons[0] != "NoFreeSlot" {
		t.Fatalf("events = %v, want NoFreeSlot on each of 3 attempts", reasons)
	}
	if h.api.Events[0].Type != "Warning" {
		t.Fatalf("NoFreeSlot type = %q, want Warning", h.api.Events[0].Type)
	}
	for _, d := range h.sleeps {
		if d != 30*time.Second {
			t.Fatalf("retry interval = %v, want 30s", d)
		}
	}
}

func TestRenewEvery30sWritesAnnotations(t *testing.T) {
	h, ctx := newHarness(t, "node-b", 2)
	h.obs = Observation{Annotations: map[string]string{"oci-link.nacfson.io/fluent-bit-buffer-bytes": "4096"}}

	if err := h.agent.Run(ctx); err != nil {
		t.Fatal(err)
	}
	if len(h.tunnel.ups) != 1 || h.tunnel.ups[0] != 0 {
		t.Fatalf("tunnel ups = %v, want [0]", h.tunnel.ups)
	}
	for _, d := range h.sleeps {
		if d != 30*time.Second {
			t.Fatalf("renew interval = %v, want 30s", d)
		}
	}
	l := h.api.Lease("wg-slot-0")
	if got := l.Metadata.Annotations["oci-link.nacfson.io/fluent-bit-buffer-bytes"]; got != "4096" {
		t.Fatalf("buffer annotation = %q, want 4096", got)
	}
	if *l.Spec.LeaseDurationSeconds != 90 {
		t.Fatalf("lease duration = %d, want 90", *l.Spec.LeaseDurationSeconds)
	}
}

func TestReleaseOnStopTearsDownThenClearsHolder(t *testing.T) {
	h, ctx := newHarness(t, "node-b", 1)

	if err := h.agent.Run(ctx); err != nil {
		t.Fatal(err)
	}
	if h.tunnel.downs != 1 {
		t.Fatalf("tunnel downs = %d, want 1", h.tunnel.downs)
	}
	if got := Holder(h.api.Lease("wg-slot-0")); got != "" {
		t.Fatalf("holder after release = %q, want empty", got)
	}
}

func TestSlotLeavingPoolMovesToAnother(t *testing.T) {
	h, ctx := newHarness(t, "node-b", 3)
	h.agent.Sleep = func(ctx context.Context, d time.Duration) error {
		h.sleeps = append(h.sleeps, d)
		if len(h.sleeps) == 1 {
			h.pool = []int{1}
		}
		if len(h.sleeps) >= h.ticks {
			h.cancel()
			return errors.New("stopped")
		}
		return nil
	}

	if err := h.agent.Run(ctx); err != nil {
		t.Fatal(err)
	}
	if len(h.tunnel.ups) != 2 || h.tunnel.ups[0] != 0 || h.tunnel.ups[1] != 1 {
		t.Fatalf("tunnel ups = %v, want [0 1]", h.tunnel.ups)
	}
	if got := Holder(h.api.Lease("wg-slot-0")); got != "" {
		t.Fatalf("slot 0 still held by %q after leaving the pool", got)
	}
}

func TestStaleTunnelEmitsOnceThenRecovers(t *testing.T) {
	h, ctx := newHarness(t, "node-b", 4)
	h.agent.Sleep = func(ctx context.Context, d time.Duration) error {
		h.sleeps = append(h.sleeps, d)
		h.obs.Stale = len(h.sleeps) < 3
		if len(h.sleeps) >= h.ticks {
			h.cancel()
			return errors.New("stopped")
		}
		return nil
	}
	h.obs.Stale = true

	if err := h.agent.Run(ctx); err != nil {
		t.Fatal(err)
	}
	reasons := h.api.EventReasons()
	if len(reasons) != 2 || reasons[0] != "TunnelStale" || reasons[1] != "TunnelRecovered" {
		t.Fatalf("events = %v, want [TunnelStale TunnelRecovered]", reasons)
	}
}

func TestParsePool(t *testing.T) {
	got, err := ParsePool("3, 1,0,1")
	if err != nil || len(got) != 3 || got[0] != 0 || got[2] != 3 {
		t.Fatalf("ParsePool = %v, %v", got, err)
	}
	if _, err := ParsePool("0,14"); err == nil {
		t.Fatal("slot 14 accepted, want an error")
	}
}

type timedTunnel struct {
	fakeTunnel
	now    func() time.Time
	downAt []time.Time
}

func (f *timedTunnel) Down() error {
	f.downAt = append(f.downAt, f.now())
	return f.fakeTunnel.Down()
}

func TestTunnelGoesDownBeforeAnUnrenewedLeaseCanExpire(t *testing.T) {
	h, ctx := newHarness(t, "node-b", 5)
	now := t0
	clock := func() time.Time { return now }
	h.agent.Claimer.Now = clock
	tun := &timedTunnel{now: clock}
	h.agent.Tunnel = tun
	h.agent.Sleep = func(ctx context.Context, d time.Duration) error {
		h.sleeps = append(h.sleeps, d)
		now = now.Add(d)
		h.api.FailPuts = true
		if len(h.sleeps) >= h.ticks {
			h.cancel()
			return errors.New("stopped")
		}
		return nil
	}
	if err := h.agent.Run(ctx); err != nil {
		t.Fatal(err)
	}
	if len(tun.downAt) == 0 {
		t.Fatal("tunnel stayed up although the lease could not be renewed")
	}
	held := tun.downAt[0].Sub(t0)
	if held >= LeaseDuration || held < LeaseDuration-2*RenewInterval {
		t.Fatalf("tunnel went down after %v; it must go down before the %v lease can expire, and not on the first failure", held, LeaseDuration)
	}
}
