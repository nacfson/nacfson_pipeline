package main

import (
	"context"
	"crypto/ecdh"
	"crypto/rand"
	"encoding/base64"
	"errors"
	"fmt"
	"sort"
	"strconv"
	"strings"
	"time"

	"nacfson_pipeline/services/wg-slot-agent/internal/kube"
	"nacfson_pipeline/services/wg-slot-agent/internal/slots"
	"nacfson_pipeline/services/wg-slot-agent/internal/wg"
)

const (
	keysSecret   = "wg-slot-keys"
	publicConfig = "wg-slot-public"
)

type keyAPI interface {
	GetSecret(ctx context.Context, name string) (kube.Secret, error)
	PutSecret(ctx context.Context, s kube.Secret) error
	GetConfigMap(ctx context.Context, name string) (kube.ConfigMap, error)
	PutConfigMap(ctx context.Context, cm kube.ConfigMap) error
	GetLease(ctx context.Context, name string) (kube.Lease, error)
}

func keyName(slot int) string     { return "slot-" + strconv.Itoa(slot) + ".key" }
func pubName(slot int) string     { return "slot-" + strconv.Itoa(slot) + ".pub" }
func addressName(slot int) string { return "slot-" + strconv.Itoa(slot) + ".address" }

func publicKey(private []byte) (string, error) {
	raw, err := base64.StdEncoding.DecodeString(strings.TrimSpace(string(private)))
	if err != nil {
		return "", err
	}
	key, err := ecdh.X25519().NewPrivateKey(raw)
	if err != nil {
		return "", err
	}
	return base64.StdEncoding.EncodeToString(key.PublicKey().Bytes()), nil
}

func newPrivateKey() ([]byte, error) {
	key, err := ecdh.X25519().GenerateKey(rand.Reader)
	if err != nil {
		return nil, err
	}
	return []byte(base64.StdEncoding.EncodeToString(key.Bytes())), nil
}

func keyedSlots(data map[string][]byte) []int {
	var out []int
	for k := range data {
		if n, ok := strings.CutSuffix(strings.TrimPrefix(k, "slot-"), ".key"); ok && strings.HasPrefix(k, "slot-") {
			if v, err := strconv.Atoi(n); err == nil {
				out = append(out, v)
			}
		}
	}
	sort.Ints(out)
	return out
}

func keygen(ctx context.Context, api keyAPI, pool []int, now time.Time) error {
	secret, err := api.GetSecret(ctx, keysSecret)
	if errors.Is(err, kube.ErrNotFound) {
		secret = kube.Secret{Metadata: kube.ObjectMeta{Name: keysSecret}, Type: "Opaque"}
	} else if err != nil {
		return err
	}
	cm, err := api.GetConfigMap(ctx, publicConfig)
	if errors.Is(err, kube.ErrNotFound) {
		cm = kube.ConfigMap{Metadata: kube.ObjectMeta{Name: publicConfig}}
	} else if err != nil {
		return err
	}
	if secret.Data == nil {
		secret.Data = map[string][]byte{}
	}
	if cm.Data == nil {
		cm.Data = map[string]string{}
	}
	listed := map[int]bool{}
	for _, slot := range pool {
		listed[slot] = true
		priv, ok := secret.Data[keyName(slot)]
		if !ok {
			if priv, err = newPrivateKey(); err != nil {
				return err
			}
			secret.Data[keyName(slot)] = priv
		}
		pub, err := publicKey(priv)
		if err != nil {
			return fmt.Errorf("slot %d: %w", slot, err)
		}
		cm.Data[pubName(slot)] = pub
		cm.Data[addressName(slot)] = wg.SlotAddress(slot) + "/32"
	}
	var held []string
	for _, slot := range keyedSlots(secret.Data) {
		if listed[slot] {
			continue
		}
		lease, err := api.GetLease(ctx, slots.LeaseName(slot))
		if err != nil && !errors.Is(err, kube.ErrNotFound) {
			return err
		}
		if err == nil && slots.Live(lease, now) {
			held = append(held, fmt.Sprintf("slot %d (held by %s)", slot, slots.Holder(lease)))
			continue
		}
		delete(secret.Data, keyName(slot))
		delete(cm.Data, pubName(slot))
		delete(cm.Data, addressName(slot))
	}
	if err := api.PutSecret(ctx, secret); err != nil {
		return err
	}
	if err := api.PutConfigMap(ctx, cm); err != nil {
		return err
	}
	if len(held) > 0 {
		return fmt.Errorf("not removed while in use, retry later: %s", strings.Join(held, ", "))
	}
	return nil
}
