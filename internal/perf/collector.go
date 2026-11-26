package perf

import (
	"context"
	"sync"
	"time"
)

// Sampler measures one target, whatever the platform.
type Sampler interface {
	// Name labels the target in the UI and the report.
	Name() string
	// Start prepares the counters the first interval needs.
	Start(ctx context.Context) error
	// Sample takes one interval's measurements.
	Sample(ctx context.Context) (Sample, error)
	// Stop releases whatever Start took.
	Stop() error
}

// Collector samples a target in the background and keeps the samples for
// the UI to draw and for the run to be saved.
type Collector struct {
	mu       sync.Mutex
	sampler  Sampler
	interval time.Duration
	samples  []Sample
	lastErr  error
	onSample func(Sample)
}

// NewCollector returns a collector sampling s every interval.
func NewCollector(s Sampler, interval time.Duration) *Collector {
	if interval < 200*time.Millisecond {
		interval = 200 * time.Millisecond
	}
	return &Collector{sampler: s, interval: interval}
}

// OnSample sets the callback called after each sample, outside the lock.
func (c *Collector) OnSample(fn func(Sample)) {
	c.mu.Lock()
	c.onSample = fn
	c.mu.Unlock()
}

// Sampler is the sampler this collector drives.
func (c *Collector) Sampler() Sampler { return c.sampler }

// Run samples until ctx is done, then stops the sampler.
func (c *Collector) Run(ctx context.Context) {
	if err := c.sampler.Start(ctx); err != nil {
		c.setErr(err)
		return
	}
	defer c.sampler.Stop()
	ticker := time.NewTicker(c.interval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			sample, err := c.sampler.Sample(ctx)
			if err != nil {
				c.setErr(err)
				continue
			}
			c.mu.Lock()
			c.lastErr = nil
			c.samples = append(c.samples, sample)
			fn := c.onSample
			c.mu.Unlock()
			if fn != nil {
				fn(sample)
			}
		}
	}
}

func (c *Collector) setErr(err error) {
	c.mu.Lock()
	c.lastErr = err
	c.mu.Unlock()
}

// Err is the last sampling error, nil once a sample succeeds again.
func (c *Collector) Err() error {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.lastErr
}

// Snapshot copies the samples taken so far.
func (c *Collector) Snapshot() []Sample {
	c.mu.Lock()
	defer c.mu.Unlock()
	out := make([]Sample, len(c.samples))
	copy(out, c.samples)
	return out
}
