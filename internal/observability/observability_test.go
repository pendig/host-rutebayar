package observability

import (
	"testing"
	"time"
)

func TestCollectorInc(t *testing.T) {
	c := NewCollector()
	c.Inc("payments.create")
	c.Inc("payments.create")
	if c.Get("payments.create") != 2 {
		t.Fatalf("expected 2, got %d", c.Get("payments.create"))
	}
}

func TestAuditTrail(t *testing.T) {
	a := NewAuditTrail()
	a.Append("payment_created", "ref-1", map[string]string{"provider": "xendit"})
	a.Append("payment_updated", "ref-1", nil)
	if a.Len() != 2 {
		t.Fatalf("expected 2 events, got %d", a.Len())
	}
	if a.Last().ReferenceID != "ref-1" {
		t.Fatalf("unexpected last reference")
	}
}

func TestRetryPolicy(t *testing.T) {
	policy := DefaultRetryPolicy()
	d1 := policy.DelayForAttempt(1)
	d2 := policy.DelayForAttempt(2)
	if d1 <= 0 || d2 <= d1 {
		t.Fatalf("invalid delays %s %s", d1, d2)
	}
}

func TestDeadLetterQueue(t *testing.T) {
	dlq := NewDeadLetterQueue()
	dlq.Push(DeadLetterItem{At: time.Now(), Reference: "r1", Provider: "x", Reason: "timeout"})
	dlq.Push(DeadLetterItem{At: time.Now(), Reference: "r2", Provider: "y", Reason: "invalid"})
	if dlq.Len() != 2 {
		t.Fatalf("expected 2")
	}
	drain := dlq.Drain()
	if len(drain) != 2 {
		t.Fatalf("expected 2 drained")
	}
	if dlq.Len() != 0 {
		t.Fatal("expected dlq emptied after drain")
	}
}
