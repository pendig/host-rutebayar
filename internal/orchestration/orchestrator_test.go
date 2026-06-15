package orchestration

import (
	"encoding/json"
	"testing"

	"github.com/pendig/host-rutebayar/internal/domain"
)

func TestCreatePaymentSuccess(t *testing.T) {
	registry := NewRegistry()
	repository := NewOrchestrator(registry)
	host := domain.Host{ID: "h-1", Name: "Host 1", NotificationKey: "notif", HostSecret: "hs", WebhookSecret: "ws"}
	registry.Hosts[host.ID] = host
	repository.registry.HostPolicies[host.ID] = domain.FeePolicy{Type: domain.FeeTypePercent, Value: 2, Currency: "IDR", Rounding: domain.RoundingRuleNearest}
	product := domain.Product{ID: "p-1", HostID: "h-1", Name: "Prod 1", Price: 100000, IsActive: true}
	repository.registry.Products[product.ID] = product
	repository.registry.HostProviderAccts["h-1"] = []domain.HostProviderAccount{{HostID: "h-1", Provider: "xendit", Env: "sandbox", CredentialsHash: "secret"}}

	out, err := repository.CreatePayment(CreateInput{HostID: "h-1", ProductID: "p-1", Env: "sandbox", BuyerRef: "buyer-1"})
	if err != nil {
		t.Fatalf("expected create success: %v", err)
	}
	if out.Order.HostID != "h-1" || out.Order.Reference == "" || out.Order.Provider != "xendit" {
		t.Fatalf("unexpected order data: %+v", out.Order)
	}
	if out.Order.HostFeeAmount != 2000 {
		t.Fatalf("unexpected host fee: %d", out.Order.HostFeeAmount)
	}
	if out.Order.ProviderFeeAmount != 2500 {
		t.Fatalf("unexpected provider fee: %d", out.Order.ProviderFeeAmount)
	}
	if out.Order.NetAmount != 95500 {
		t.Fatalf("unexpected net: %d", out.Order.NetAmount)
	}

	_, err = repository.GetPayment(out.Reference)
	if err != nil {
		t.Fatalf("expected order stored: %v", err)
	}

	ledger, err := repository.GetLedger(out.Reference)
	if err != nil {
		t.Fatalf("expected ledger stored: %v", err)
	}
	if ledger.PaymentOrderID != out.Reference {
		t.Fatalf("ledger mismatch")
	}
}

func TestCreatePaymentValidation(t *testing.T) {
	registry := NewRegistry()
	registry.Hosts["h-1"] = domain.Host{ID: "h-1", Name: "Host 1", NotificationKey: "notif", HostSecret: "hs", WebhookSecret: "ws"}
	repository := NewOrchestrator(registry)

	if _, err := repository.CreatePayment(CreateInput{HostID: "unknown", ProductID: "p-1"}); err == nil {
		t.Fatal("expected host not found")
	}
}

func TestReconcileWebhookIdempotent(t *testing.T) {
	registry := NewRegistry()
	repository := NewOrchestrator(registry)
	repository.registry.Hosts["h-1"] = domain.Host{ID: "h-1", Name: "Host 1", NotificationKey: "notif", HostSecret: "hs", WebhookSecret: "ws"}
	repository.registry.HostPolicies["h-1"] = domain.FeePolicy{Type: domain.FeeTypeFree, Value: 0, Currency: "IDR", Rounding: domain.RoundingRuleNearest}
	repository.registry.Products["p-1"] = domain.Product{ID: "p-1", HostID: "h-1", Name: "Prod", Price: 10000, IsActive: true}
	repository.registry.HostProviderAccts["h-1"] = []domain.HostProviderAccount{{HostID: "h-1", Provider: "midtrans", Env: "sandbox", CredentialsHash: "hash"}}

	out, err := repository.CreatePayment(CreateInput{HostID: "h-1", ProductID: "p-1"})
	if err != nil {
		t.Fatalf("create failed: %v", err)
	}
	status, err := repository.ReconcileWebhook(out.Reference, "midtrans", string(domain.PaymentOrderStatusSuccess), "idem-1")
	if err != nil || status != domain.PaymentOrderStatusSuccess {
		t.Fatalf("expected success status, got %v, err=%v", status, err)
	}
	status2, err := repository.ReconcileWebhook(out.Reference, "midtrans", string(domain.PaymentOrderStatusFailed), "idem-1")
	if err != nil || status2 != domain.PaymentOrderStatusSuccess {
		t.Fatalf("expected idempotent success status, got %v, err=%v", status2, err)
	}
}

func TestReferenceIsJSONSafe(t *testing.T) {
	registry := NewRegistry()
	repository := NewOrchestrator(registry)
	repository.registry.Hosts["h-1"] = domain.Host{ID: "h-1", Name: "Host 1", NotificationKey: "notif", HostSecret: "hs", WebhookSecret: "ws"}
	repository.registry.Products["p-1"] = domain.Product{ID: "p-1", HostID: "h-1", Name: "Prod", Price: 10000, IsActive: true}
	repository.registry.HostPolicies["h-1"] = domain.FeePolicy{Type: domain.FeeTypePercent, Value: 10, Currency: "IDR"}
	repository.registry.HostProviderAccts["h-1"] = []domain.HostProviderAccount{{HostID: "h-1", Provider: "xendit", Env: "sandbox", CredentialsHash: "hash"}}

	out, err := repository.CreatePayment(CreateInput{HostID: "h-1", ProductID: "p-1", Env: "sandbox"})
	if err != nil {
		t.Fatalf("create failed: %v", err)
	}
	payload, err := json.Marshal(out.Order)
	if err != nil {
		t.Fatalf("order should marshal to json: %v", err)
	}
	if len(payload) == 0 {
		t.Fatal("empty json payload")
	}
}
