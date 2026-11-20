package main

import (
	"context"
	"fmt"
	"log"
	"os"
	"os/signal"
	"path/filepath"
	"syscall"
	"time"

	"nacfson_pipeline/services/wg-slot-agent/internal/kube"
	"nacfson_pipeline/services/wg-slot-agent/internal/metrics"
	"nacfson_pipeline/services/wg-slot-agent/internal/slots"
	"nacfson_pipeline/services/wg-slot-agent/internal/wg"
)

func env(key, fallback string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return fallback
}

func readPool(path string) ([]int, error) {
	b, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	return slots.ParsePool(string(b))
}

func main() {
	log.SetFlags(0)
	if len(os.Args) != 2 || (os.Args[1] != "run" && os.Args[1] != "keygen") {
		fmt.Fprintln(os.Stderr, "usage: wg-slot-agent run|keygen")
		os.Exit(2)
	}
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGTERM, syscall.SIGINT)
	defer stop()
	client, err := kube.InCluster()
	if err != nil {
		log.Fatal(err)
	}
	poolFile := env("POOL_FILE", "/etc/wg-slot-pool/slots")
	pool, err := readPool(poolFile)
	if err != nil {
		log.Fatalf("pool %s: %v", poolFile, err)
	}
	if os.Args[1] == "keygen" {
		if err := keygen(ctx, client, pool, time.Now()); err != nil {
			log.Fatal(err)
		}
		log.Printf("keys ready for slots %v", pool)
		return
	}
	if err := run(ctx, client, poolFile); err != nil {
		log.Fatal(err)
	}
}

func run(ctx context.Context, client *kube.Client, poolFile string) error {
	node := os.Getenv("NODE_NAME")
	if node == "" {
		return fmt.Errorf("NODE_NAME must be set")
	}
	pod := os.Getenv("POD_NAME")
	if pod == "" {
		return fmt.Errorf("POD_NAME must be set")
	}
	mode, err := wg.ParseMode(os.Getenv("WG_MODE"))
	if err != nil {
		return err
	}
	keysDir := env("KEYS_DIR", "/etc/wg-slot-keys")
	peersFile := env("PEERS_FILE", "/etc/wg-peers/peers.json")
	bufferDir := env("BUFFER_DIR", "/var/lib/fluent-bit-state/chunks")
	keyFile := func(slot int) string { return filepath.Join(keysDir, keyName(slot)) }
	runner := wg.Exec{}
	agent := &slots.Agent{
		Claimer: &slots.Claimer{API: client, Node: node, Now: time.Now},
		Pool: func() ([]int, error) {
			listed, err := readPool(poolFile)
			if err != nil {
				return nil, err
			}
			var keyed []int
			for _, s := range listed {
				if _, err := os.Stat(keyFile(s)); err == nil {
					keyed = append(keyed, s)
				}
			}
			return keyed, nil
		},
		Tunnel: &wg.Tunnel{
			Runner: runner,
			Mode:   mode,
			VCN:    env("VCN_CIDR", "10.200.0.0/24"),
			Peers: func() ([]wg.Peer, error) {
				b, err := os.ReadFile(peersFile)
				if err != nil {
					return nil, err
				}
				return wg.ParsePeers(b)
			},
			KeyFile: keyFile,
		},
		Event: func(ctx context.Context, eventType, reason, message string) error {
			return client.CreateEvent(ctx, kube.ObjectReference{APIVersion: "v1", Kind: "Pod", Name: pod, Namespace: client.Namespace}, eventType, reason, message, node, time.Now())
		},
		Observe: func() slots.Observation {
			out, err := runner.Output("wg", "show", wg.Interface, "latest-handshakes")
			if err != nil {
				log.Printf("wg show: %v", err)
			}
			h := metrics.ParseLatestHandshakes(out, time.Now())
			return slots.Observation{Annotations: h.Annotations(metrics.BufferBytes(bufferDir)), Stale: h.Stale}
		},
		Sleep: func(ctx context.Context, d time.Duration) error {
			select {
			case <-ctx.Done():
				return ctx.Err()
			case <-time.After(d):
				return nil
			}
		},
	}
	log.Printf("wg-slot-agent on %s, mode %s", node, mode)
	return agent.Run(ctx)
}
