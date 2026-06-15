package observability

import (
	"fmt"
	"math"
	"sync"
	"time"
)

// Collector keeps lightweight counters for runtime metrics.
type Collector struct {
	mu       sync.Mutex
	counters map[string]int64
}

func NewCollector() *Collector {
	return &Collector{counters: map[string]int64{}}
}

func (c *Collector) Inc(key string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.counters[key] = c.counters[key] + 1
}

func (c *Collector) Get(key string) int64 {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.counters[key]
}

// AuditEvent stores immutable event facts.
type AuditEvent struct {
	At          time.Time
	Event       string
	ReferenceID string
	Details     map[string]string
}

// AuditTrail stores append-only audit log.
type AuditTrail struct {
	mu     sync.Mutex
	events []AuditEvent
}

func NewAuditTrail() *AuditTrail {
	return &AuditTrail{events: []AuditEvent{}}
}

func (a *AuditTrail) Append(event, referenceID string, details map[string]string) {
	a.mu.Lock()
	defer a.mu.Unlock()
	copyDetails := map[string]string{}
	for key, value := range details {
		copyDetails[key] = value
	}
	a.events = append(a.events, AuditEvent{At: time.Now().UTC(), Event: event, ReferenceID: referenceID, Details: copyDetails})
}

func (a *AuditTrail) Len() int {
	a.mu.Lock()
	defer a.mu.Unlock()
	return len(a.events)
}

func (a *AuditTrail) Last() AuditEvent {
	a.mu.Lock()
	defer a.mu.Unlock()
	if len(a.events) == 0 {
		return AuditEvent{}
	}
	last := a.events[len(a.events)-1]
	copyDetails := map[string]string{}
	for key, value := range last.Details {
		copyDetails[key] = value
	}
	return AuditEvent{At: last.At, Event: last.Event, ReferenceID: last.ReferenceID, Details: copyDetails}
}

// RetryPolicy drives webhook callback retries.
type RetryPolicy struct {
	MaxAttempts int
	BaseDelay   time.Duration
	MaxDelay    time.Duration
	Multiplier  float64
}

func DefaultRetryPolicy() RetryPolicy {
	return RetryPolicy{MaxAttempts: 3, BaseDelay: 200 * time.Millisecond, MaxDelay: 5 * time.Second, Multiplier: 2}
}

func (p RetryPolicy) DelayForAttempt(attempt int) time.Duration {
	if attempt <= 0 {
		attempt = 1
	}
	base := float64(p.BaseDelay)
	// exponent grows 0,1,2...
	delay := time.Duration(base * math.Pow(p.Multiplier, float64(attempt-1)))
	if p.MaxDelay > 0 && delay > p.MaxDelay {
		return p.MaxDelay
	}
	return delay
}

// DeadLetterItem stores failed callback payload to retry later.
type DeadLetterItem struct {
	At        time.Time
	Reference string
	Provider  string
	Reason    string
	Payload   string
}

// DeadLetterQueue is simple in-memory DLQ.
type DeadLetterQueue struct {
	mu    sync.Mutex
	items []DeadLetterItem
}

func NewDeadLetterQueue() *DeadLetterQueue {
	return &DeadLetterQueue{items: []DeadLetterItem{}}
}

func (d *DeadLetterQueue) Push(item DeadLetterItem) {
	d.mu.Lock()
	defer d.mu.Unlock()
	d.items = append(d.items, item)
}

func (d *DeadLetterQueue) Drain() []DeadLetterItem {
	d.mu.Lock()
	defer d.mu.Unlock()
	items := make([]DeadLetterItem, len(d.items))
	copy(items, d.items)
	d.items = []DeadLetterItem{}
	return items
}

func (d *DeadLetterQueue) Len() int {
	d.mu.Lock()
	defer d.mu.Unlock()
	return len(d.items)
}

func (d *DeadLetterQueue) Summary() string {
	d.mu.Lock()
	defer d.mu.Unlock()
	return fmt.Sprintf("dead-letter size=%d", len(d.items))
}
