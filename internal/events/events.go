// Package events carries VisitEvents from redirectors to the aggregation worker.
//
//   - Channel: in-process buffered channel (single binary, no Redis).
//   - Redis Streams: XADD tusk:visits / consumer group "workers" (split mode or REDIS_ENABLED).
//
// Publish never blocks: when the local buffer is full the event is dropped and counted.
package events

import (
	"context"
	"log/slog"
	"sync/atomic"
	"time"

	"github.com/redis/go-redis/v9"

	"github.com/Hulul-Group/Tusk/internal/config"
	"github.com/Hulul-Group/Tusk/internal/domain"
)

// New picks the transport: Redis Streams when rdb != nil and (split mode or REDIS_ENABLED),
// else the in-process channel. Both return values share the same underlying transport.
func New(cfg *config.Config, rdb *redis.Client) (domain.VisitPublisher, domain.VisitConsumer) {
	if rdb != nil && (cfg.SplitMode() || cfg.RedisEnabled) {
		s := NewStream(rdb, StreamOptions{
			BufferSize:    cfg.VisitBufferSize,
			BatchSize:     cfg.VisitBatchSize,
			FlushInterval: cfg.VisitFlushInterval,
		})
		return s, s
	}
	c := NewChannel(cfg.VisitBufferSize, cfg.VisitBatchSize, cfg.VisitFlushInterval)
	return c, c
}

// Stats exposes counters for /metrics.
type Stats interface {
	Dropped() uint64
	Published() uint64
}

// Channel is the in-process transport.
type Channel struct {
	ch        chan domain.VisitEvent
	batch     int
	interval  time.Duration
	closed    atomic.Bool
	dropped   atomic.Uint64
	published atomic.Uint64
}

// NewChannel creates a channel transport.
func NewChannel(buffer, batch int, interval time.Duration) *Channel {
	if buffer <= 0 {
		buffer = 100_000
	}
	if batch <= 0 {
		batch = 500
	}
	if interval <= 0 {
		interval = time.Second
	}
	return &Channel{ch: make(chan domain.VisitEvent, buffer), batch: batch, interval: interval}
}

// Publish enqueues without blocking; drops when full or closed.
func (c *Channel) Publish(ev domain.VisitEvent) {
	if c.closed.Load() {
		c.dropped.Add(1)
		return
	}
	select {
	case c.ch <- ev:
		c.published.Add(1)
	default:
		c.dropped.Add(1)
	}
}

// Close stops accepting events; the consumer drains what's buffered when its ctx ends.
func (c *Channel) Close(context.Context) error {
	c.closed.Store(true)
	return nil
}

func (c *Channel) Dropped() uint64   { return c.dropped.Load() }
func (c *Channel) Published() uint64 { return c.published.Load() }
func (c *Channel) Len() int          { return len(c.ch) }

// Run delivers batches of up to batch events, at least every interval when non-empty.
// On ctx cancellation it drains the buffer and delivers the rest with a fresh 10s context.
// A failing handler is retried (3 attempts) before the batch is dropped and logged: the
// in-process channel has no durable storage to fall back on.
func (c *Channel) Run(ctx context.Context, handle func(context.Context, []domain.VisitEvent) error) error {
	buf := make([]domain.VisitEvent, 0, c.batch)
	t := time.NewTicker(c.interval)
	defer t.Stop()
	deliver := func(ctx context.Context) {
		if len(buf) == 0 {
			return
		}
		var err error
		for attempt := 0; attempt < 3; attempt++ {
			if err = handle(ctx, buf); err == nil {
				break
			}
			if ctx.Err() != nil {
				break
			}
			time.Sleep(time.Duration(attempt+1) * 200 * time.Millisecond)
		}
		if err != nil {
			slog.Error("events: dropping visit batch after handler errors", "events", len(buf), "err", err)
			c.dropped.Add(uint64(len(buf)))
		}
		buf = buf[:0]
	}
	for {
		select {
		case <-ctx.Done():
			fctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
			for {
				select {
				case ev := <-c.ch:
					buf = append(buf, ev)
					if len(buf) >= c.batch {
						deliver(fctx)
					}
					continue
				default:
				}
				break
			}
			deliver(fctx)
			cancel()
			return nil
		case ev := <-c.ch:
			buf = append(buf, ev)
			if len(buf) >= c.batch {
				deliver(ctx)
			}
		case <-t.C:
			deliver(ctx)
		}
	}
}
