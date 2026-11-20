package main

import (
	"bytes"
	"context"
	"testing"
	"time"

	"nacfson_pipeline/services/wg-slot-agent/internal/kube"
	"nacfson_pipeline/services/wg-slot-agent/internal/kubetest"
)

var now = time.Date(2026, 11, 18, 12, 0, 0, 0, time.UTC)

func runKeygen(t *testing.T, api *kubetest.FakeAPI, pool ...int) error {
	t.Helper()
	return keygen(context.Background(), api.Client(), pool, now)
}

func TestKeygenFirstRun(t *testing.T) {
	api := kubetest.NewFakeAPI()
	defer api.Close()
	if err := runKeygen(t, api, 0, 1, 2, 3); err != nil {
		t.Fatal(err)
	}
	s, cm := api.Secrets[keysSecret], api.ConfigMaps[publicConfig]
	if len(s.Data) != 4 || len(cm.Data) != 8 {
		t.Fatalf("got %d keys and %d public entries, want 4 and 8", len(s.Data), len(cm.Data))
	}
	for slot := 0; slot < 4; slot++ {
		pub, err := publicKey(s.Data[keyName(slot)])
		if err != nil || pub != cm.Data[pubName(slot)] {
			t.Fatalf("slot %d public key does not match its private key: %v", slot, err)
		}
	}
	if cm.Data["slot-3.address"] != "10.201.0.4/32" {
		t.Fatalf("slot-3.address = %q", cm.Data["slot-3.address"])
	}
}

func TestKeygenAddsSlotAndKeepsExistingKeys(t *testing.T) {
	api := kubetest.NewFakeAPI()
	defer api.Close()
	if err := runKeygen(t, api, 0, 1); err != nil {
		t.Fatal(err)
	}
	before := bytes.Clone(api.Secrets[keysSecret].Data[keyName(0)])
	if err := runKeygen(t, api, 0, 1, 4); err != nil {
		t.Fatal(err)
	}
	s := api.Secrets[keysSecret]
	if !bytes.Equal(s.Data[keyName(0)], before) {
		t.Fatal("existing slot 0 key was overwritten")
	}
	if _, ok := s.Data[keyName(4)]; !ok {
		t.Fatal("slot 4 key was not added")
	}
}

func TestKeygenRemovesFreeUnlistedSlot(t *testing.T) {
	api := kubetest.NewFakeAPI()
	defer api.Close()
	if err := runKeygen(t, api, 0, 1); err != nil {
		t.Fatal(err)
	}
	holder, d := "gone-node", int32(90)
	stale := now.Add(-5 * time.Minute).Format(kube.MicroTimeLayout)
	api.PutLease(kube.Lease{Metadata: kube.ObjectMeta{Name: "wg-slot-1"}, Spec: kube.LeaseSpec{HolderIdentity: &holder, LeaseDurationSeconds: &d, RenewTime: &stale}})
	if err := runKeygen(t, api, 0); err != nil {
		t.Fatal(err)
	}
	if _, ok := api.Secrets[keysSecret].Data[keyName(1)]; ok {
		t.Fatal("unlisted free slot 1 key kept")
	}
	if _, ok := api.ConfigMaps[publicConfig].Data[pubName(1)]; ok {
		t.Fatal("unlisted free slot 1 public key kept")
	}
}

func TestKeygenRefusesToRemoveHeldSlot(t *testing.T) {
	api := kubetest.NewFakeAPI()
	defer api.Close()
	if err := runKeygen(t, api, 0, 1); err != nil {
		t.Fatal(err)
	}
	holder, d := "node-a", int32(90)
	renewed := now.Add(-10 * time.Second).Format(kube.MicroTimeLayout)
	api.PutLease(kube.Lease{Metadata: kube.ObjectMeta{Name: "wg-slot-1"}, Spec: kube.LeaseSpec{HolderIdentity: &holder, LeaseDurationSeconds: &d, RenewTime: &renewed}})
	if err := runKeygen(t, api, 0, 2); err == nil {
		t.Fatal("keygen succeeded while slot 1 is held")
	}
	s := api.Secrets[keysSecret]
	if _, ok := s.Data[keyName(1)]; !ok {
		t.Fatal("held slot 1 key removed")
	}
	if _, ok := s.Data[keyName(2)]; !ok {
		t.Fatal("slot 2 was not added while slot 1 blocked removal")
	}
}
