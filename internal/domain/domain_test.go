package domain

import "testing"

func TestFeePolicyValidation(t *testing.T) {
	if err := (FeePolicy{Type: FeeTypePercent, Value: 2.5, Currency: "IDR"}.Validate()); err != nil {
		t.Fatalf("expected valid percent policy, got: %v", err)
	}

	if err := (FeePolicy{Type: FeeTypePercent, Value: 120, Currency: "IDR"}.Validate()); err == nil {
		t.Fatal("expected error for percent > 100")
	}

	if err := (FeePolicy{Type: FeeTypeFixed, Value: 100, Currency: "IDR"}.Validate()); err != nil {
		t.Fatalf("expected valid fixed policy, got: %v", err)
	}

	if err := (FeePolicy{Type: FeeTypeFree, Value: 1, Currency: "IDR"}.Validate()); err == nil {
		t.Fatal("expected error for free fee with non-zero value")
	}
}

func TestCalculateHostFeePercentFixed(t *testing.T) {
	policy := FeePolicy{Type: FeeTypePercent, Value: 2.5, Currency: "IDR", Rounding: RoundingRuleNearest}
	fee, err := policy.CalculateHostFee(1999)
	if err != nil {
		t.Fatalf("unexpected err: %v", err)
	}
	if fee != 50 {
		t.Fatalf("expected rounded fee 50, got %d", fee)
	}

	floorFee, err := FeePolicy{Type: FeeTypePercent, Value: 2.333, Currency: "IDR", Rounding: RoundingRuleFloor}.CalculateHostFee(1000)
	if err != nil {
		t.Fatalf("expected no err, got %v", err)
	}
	if floorFee != 23 {
		t.Fatalf("expected floor fee 23, got %d", floorFee)
	}

	ceilFee, err := FeePolicy{Type: FeeTypePercent, Value: 2.333, Currency: "IDR", Rounding: RoundingRuleCeil}.CalculateHostFee(1000)
	if err != nil {
		t.Fatalf("expected no err, got %v", err)
	}
	if ceilFee != 24 {
		t.Fatalf("expected ceil fee 24, got %d", ceilFee)
	}
}

func TestCalculateHostFeeClampAndFree(t *testing.T) {
	min := int64(100)
	max := int64(150)
	policy := FeePolicy{Type: FeeTypePercent, Value: 1, Currency: "IDR", MinFee: &min, MaxFee: &max}
	fee, err := policy.CalculateHostFee(50)
	if err != nil {
		t.Fatalf("unexpected err: %v", err)
	}
	if fee != 100 {
		t.Fatalf("expected min clamp 100, got %d", fee)
	}

	fee, err = policy.CalculateHostFee(20000)
	if err != nil {
		t.Fatalf("unexpected err: %v", err)
	}
	if fee != 150 {
		t.Fatalf("expected max clamp 150, got %d", fee)
	}

	if got, err := (FeePolicy{Type: FeeTypeFree, Value: 0, Currency: "IDR"}.CalculateHostFee(200000)); err != nil || got != 0 {
		t.Fatalf("expected free fee 0, got (%d, %v)", got, err)
	}
}

func TestHostProductValidation(t *testing.T) {
	h := Host{ID: "host-1", Name: "Host UMKM", NotificationKey: "notif", HostSecret: "h", WebhookSecret: "w"}
	if err := h.Validate(); err != nil {
		t.Fatalf("host should validate, got %v", err)
	}
	p := Product{ID: "p-1", HostID: "host-1", Name: "Starter", Price: 10000}
	if err := p.Validate(); err != nil {
		t.Fatalf("product should validate, got %v", err)
	}
}

func TestFeePolicySnapshotHash(t *testing.T) {
	a := NewFeePolicySnapshot(FeePolicy{Type: FeeTypePercent, Value: 2.5, Currency: "IDR", PolicyVersion: "v1"})
	b := NewFeePolicySnapshot(FeePolicy{Type: FeeTypePercent, Value: 2.5, Currency: "IDR", PolicyVersion: "v1"})
	if a.PolicyPayloadHash != b.PolicyPayloadHash {
		t.Fatalf("expected stable snapshot hash")
	}
}
