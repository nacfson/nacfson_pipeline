package kafkaclient

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"fmt"
	"os"
	"time"

	"github.com/twmb/franz-go/pkg/kgo"
	"github.com/twmb/franz-go/pkg/kmsg"
)

type Config struct {
	Brokers  []string
	CAFile   string
	CertFile string
	KeyFile  string
}

func (c Config) tls() (*tls.Config, error) {
	ca, err := os.ReadFile(c.CAFile)
	if err != nil {
		return nil, err
	}
	pool := x509.NewCertPool()
	if !pool.AppendCertsFromPEM(ca) {
		return nil, fmt.Errorf("no certificates in %s", c.CAFile)
	}
	return &tls.Config{
		MinVersion: tls.VersionTLS12,
		RootCAs:    pool,
		GetClientCertificate: func(*tls.CertificateRequestInfo) (*tls.Certificate, error) {
			cert, err := tls.LoadX509KeyPair(c.CertFile, c.KeyFile)
			return &cert, err
		},
	}, nil
}

func (c Config) options() ([]kgo.Opt, error) {
	t, err := c.tls()
	if err != nil {
		return nil, err
	}
	return []kgo.Opt{kgo.SeedBrokers(c.Brokers...), kgo.DialTLSConfig(t)}, nil
}

func NewPartitionReader(c Config, from map[string]map[int32]int64) (*kgo.Client, error) {
	opts, err := c.options()
	if err != nil {
		return nil, err
	}
	assign := map[string]map[int32]kgo.Offset{}
	for topic, parts := range from {
		assign[topic] = map[int32]kgo.Offset{}
		for p, off := range parts {
			assign[topic][p] = kgo.NewOffset().At(off)
		}
	}
	return kgo.NewClient(append(opts, kgo.ConsumePartitions(assign))...)
}

func NewAdmin(c Config) (*kgo.Client, error) {
	opts, err := c.options()
	if err != nil {
		return nil, err
	}
	return kgo.NewClient(opts...)
}

func listOffsets(ctx context.Context, cl *kgo.Client, topic string, timestamp int64) (map[int32]int64, error) {
	meta := kmsg.NewPtrMetadataRequest()
	mt := kmsg.NewMetadataRequestTopic()
	mt.Topic = kmsg.StringPtr(topic)
	meta.Topics = append(meta.Topics, mt)
	mresp, err := meta.RequestWith(ctx, cl)
	if err != nil {
		return nil, err
	}
	if len(mresp.Topics) != 1 || mresp.Topics[0].ErrorCode != 0 {
		return nil, fmt.Errorf("metadata for %s failed", topic)
	}
	req := kmsg.NewPtrListOffsetsRequest()
	rt := kmsg.NewListOffsetsRequestTopic()
	rt.Topic = topic
	for _, p := range mresp.Topics[0].Partitions {
		rp := kmsg.NewListOffsetsRequestTopicPartition()
		rp.Partition = p.Partition
		rp.Timestamp = timestamp
		rt.Partitions = append(rt.Partitions, rp)
	}
	req.Topics = append(req.Topics, rt)
	out := map[int32]int64{}
	for _, shard := range cl.RequestSharded(ctx, req) {
		if shard.Err != nil {
			return nil, shard.Err
		}
		for _, t := range shard.Resp.(*kmsg.ListOffsetsResponse).Topics {
			for _, p := range t.Partitions {
				if p.ErrorCode != 0 {
					return nil, fmt.Errorf("list offsets %s-%d: error code %d", topic, p.Partition, p.ErrorCode)
				}
				out[p.Partition] = p.Offset
			}
		}
	}
	return out, nil
}

func ReadSince(ctx context.Context, c Config, topic string, since time.Time) ([]*kgo.Record, map[int32]int64, error) {
	admin, err := NewAdmin(c)
	if err != nil {
		return nil, nil, err
	}
	defer admin.Close()
	ends, err := listOffsets(ctx, admin, topic, -1)
	if err != nil {
		return nil, nil, err
	}
	starts, err := listOffsets(ctx, admin, topic, since.UnixMilli())
	if err != nil {
		return nil, nil, err
	}
	assign := map[int32]kgo.Offset{}
	for p, end := range ends {
		start, ok := starts[p]
		if !ok || start < 0 || start >= end {
			continue
		}
		assign[p] = kgo.NewOffset().At(start)
	}
	if len(assign) == 0 {
		return nil, ends, nil
	}
	opts, err := c.options()
	if err != nil {
		return nil, nil, err
	}
	cl, err := kgo.NewClient(append(opts, kgo.ConsumePartitions(map[string]map[int32]kgo.Offset{topic: assign}))...)
	if err != nil {
		return nil, nil, err
	}
	defer cl.Close()
	var out []*kgo.Record
	open := len(assign)
	for open > 0 {
		fetches := cl.PollFetches(ctx)
		if err := ctx.Err(); err != nil {
			return out, ends, err
		}
		if errs := fetches.Errors(); len(errs) > 0 {
			return out, ends, errs[0].Err
		}
		fetches.EachRecord(func(r *kgo.Record) {
			if r.Offset >= ends[r.Partition] {
				return
			}
			out = append(out, r)
			if r.Offset == ends[r.Partition]-1 {
				open--
			}
		})
	}
	return out, ends, nil
}

type ClusterFacts struct {
	Brokers         int
	UnderReplicated int
	Offline         int
}

func Describe(ctx context.Context, cl *kgo.Client) (ClusterFacts, error) {
	req := kmsg.NewPtrMetadataRequest()
	resp, err := req.RequestWith(ctx, cl)
	if err != nil {
		return ClusterFacts{}, err
	}
	facts := ClusterFacts{Brokers: len(resp.Brokers)}
	for _, t := range resp.Topics {
		if t.IsInternal {
			continue
		}
		for _, p := range t.Partitions {
			if p.Leader < 0 {
				facts.Offline++
			}
			if len(p.ISR) < len(p.Replicas) {
				facts.UnderReplicated++
			}
		}
	}
	return facts, nil
}
