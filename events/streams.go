package events

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/redis/go-redis/v9"
)

// streamKey / dlqKey map an event name to its Redis Stream and dead-letter
// stream. One stream per event name (the chosen layout).
func streamKey(name string) string { return "events:" + name }
func dlqKey(name string) string    { return "events:dead:" + name }

type streams struct {
	rdb      *redis.Client
	opt      Config
	consumer string // this instance's consumer name within a group
}

func newStreams(cfg Config) (Bus, error) {
	rdb := redis.NewClient(&redis.Options{
		Addr:     cfg.RedisAddr,
		Password: cfg.RedisPassword,
		DB:       cfg.RedisDB,
	})
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := rdb.Ping(ctx).Err(); err != nil {
		return nil, fmt.Errorf("events: redis ping %s: %w", cfg.RedisAddr, err)
	}
	host, _ := os.Hostname()
	return &streams{rdb: rdb, opt: cfg, consumer: fmt.Sprintf("%s-%d", host, os.Getpid())}, nil
}

func (s *streams) Publish(ctx context.Context, e Event) error {
	e.ensureMeta()
	payload, err := json.Marshal(e.Payload)
	if err != nil {
		return fmt.Errorf("events: marshal payload: %w", err)
	}
	return s.rdb.XAdd(ctx, &redis.XAddArgs{
		Stream: streamKey(e.Name),
		MaxLen: s.opt.MaxLen,
		Approx: true, // MAXLEN ~ N: cheap approximate trim
		Values: map[string]any{
			"id":      e.ID,
			"name":    e.Name,
			"source":  e.Source,
			"tenant":  e.TenantID,
			"ts":      e.Timestamp.Format(time.RFC3339Nano),
			"payload": string(payload),
		},
	}).Err()
}

func (s *streams) Consume(ctx context.Context, group string, names []string, h Handler) error {
	if len(names) == 0 {
		return errors.New("events: Consume requires at least one event name")
	}
	// Create the group (and stream) for each name. "$" = deliver only events
	// added after the group exists; because groups are durable, events that
	// arrive while this consumer is down are delivered when it returns.
	for _, name := range names {
		err := s.rdb.XGroupCreateMkStream(ctx, streamKey(name), group, "$").Err()
		if err != nil && !isBusyGroup(err) {
			return fmt.Errorf("events: create group %q on %q: %w", group, name, err)
		}
	}

	go s.reclaimLoop(ctx, group, names, h)

	// XReadGroup wants all stream keys followed by a ">" per stream.
	args := make([]string, 0, len(names)*2)
	for _, n := range names {
		args = append(args, streamKey(n))
	}
	for range names {
		args = append(args, ">")
	}

	for {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		res, err := s.rdb.XReadGroup(ctx, &redis.XReadGroupArgs{
			Group:    group,
			Consumer: s.consumer,
			Streams:  args,
			Count:    16,
			Block:    5 * time.Second,
		}).Result()
		if err != nil {
			if errors.Is(err, redis.Nil) {
				continue // block timeout, no new events
			}
			if ctx.Err() != nil {
				return ctx.Err()
			}
			time.Sleep(time.Second) // transient (e.g. redis blip); back off
			continue
		}
		for _, st := range res {
			name := strings.TrimPrefix(st.Stream, "events:")
			for _, m := range st.Messages {
				s.handle(ctx, group, name, st.Stream, m, h)
			}
		}
	}
}

// handle dedups, runs the handler, and acks on success. On handler error the
// message is left pending — reclaimLoop redelivers it, then dead-letters it once
// it exceeds MaxDeliveries.
func (s *streams) handle(ctx context.Context, group, name, stream string, m redis.XMessage, h Handler) {
	ev, err := decode(m)
	if err != nil {
		_ = s.rdb.XAck(ctx, stream, group, m.ID).Err() // undecodable → drop
		return
	}
	dedupKey := "events:dedup:" + group + ":" + ev.ID
	if n, _ := s.rdb.Exists(ctx, dedupKey).Result(); n > 0 {
		_ = s.rdb.XAck(ctx, stream, group, m.ID).Err() // already processed
		return
	}
	if err := h(ctx, ev); err != nil {
		return // leave unacked for redelivery
	}
	// Mark processed (idempotency) then ack. A crash between these two re-runs
	// the handler on redelivery — hence handlers must be idempotent.
	s.rdb.Set(ctx, dedupKey, "1", s.opt.DedupTTL)
	_ = s.rdb.XAck(ctx, stream, group, m.ID).Err()
}

// reclaimLoop periodically reclaims events that were delivered but never acked
// (a crashed/stuck consumer), redelivering them here or dead-lettering them once
// they exceed the delivery ceiling.
func (s *streams) reclaimLoop(ctx context.Context, group string, names []string, h Handler) {
	ticker := time.NewTicker(s.opt.ClaimIdle)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			for _, name := range names {
				s.reclaimStream(ctx, group, name, h)
			}
		}
	}
}

func (s *streams) reclaimStream(ctx context.Context, group, name string, h Handler) {
	stream := streamKey(name)
	pending, err := s.rdb.XPendingExt(ctx, &redis.XPendingExtArgs{
		Stream: stream,
		Group:  group,
		Idle:   s.opt.ClaimIdle,
		Start:  "-",
		End:    "+",
		Count:  64,
	}).Result()
	if err != nil {
		return
	}
	for _, p := range pending {
		if p.RetryCount > s.opt.MaxDeliveries {
			s.deadLetter(ctx, group, name, stream, p.ID)
			continue
		}
		msgs, err := s.rdb.XClaim(ctx, &redis.XClaimArgs{
			Stream:   stream,
			Group:    group,
			Consumer: s.consumer,
			MinIdle:  s.opt.ClaimIdle,
			Messages: []string{p.ID},
		}).Result()
		if err != nil {
			continue
		}
		for _, m := range msgs {
			s.handle(ctx, group, name, stream, m, h)
		}
	}
}

// deadLetter copies a poison event to its dead-letter stream and acks the
// original so it stops blocking the group.
func (s *streams) deadLetter(ctx context.Context, group, name, stream, id string) {
	msgs, err := s.rdb.XRange(ctx, stream, id, id).Result()
	if err == nil && len(msgs) == 1 {
		vals := msgs[0].Values
		vals["dead_reason"] = "max deliveries exceeded"
		vals["dead_group"] = group
		_ = s.rdb.XAdd(ctx, &redis.XAddArgs{Stream: dlqKey(name), Approx: true, MaxLen: s.opt.MaxLen, Values: vals}).Err()
	}
	_ = s.rdb.XAck(ctx, stream, group, id).Err()
}

func (s *streams) HealthCheck(ctx context.Context) error { return s.rdb.Ping(ctx).Err() }
func (s *streams) Close() error                          { return s.rdb.Close() }

func decode(m redis.XMessage) (Event, error) {
	get := func(k string) string { v, _ := m.Values[k].(string); return v }
	ev := Event{ID: get("id"), Name: get("name"), Source: get("source"), TenantID: get("tenant")}
	if ts := get("ts"); ts != "" {
		ev.Timestamp, _ = time.Parse(time.RFC3339Nano, ts)
	}
	if p := get("payload"); p != "" && p != "null" {
		_ = json.Unmarshal([]byte(p), &ev.Payload)
	}
	if ev.ID == "" || ev.Name == "" {
		return ev, errors.New("events: malformed stream entry")
	}
	return ev, nil
}

func isBusyGroup(err error) bool {
	return err != nil && strings.Contains(err.Error(), "BUSYGROUP")
}
