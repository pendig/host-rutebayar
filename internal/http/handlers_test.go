package httphandlers

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/pendig/host-rutebayar/internal/domain"
	"github.com/pendig/host-rutebayar/internal/orchestration"
)

func TestCreatePaymentHTTP(t *testing.T) {
	reg := orchestration.NewRegistry()
	reg.Hosts["h-1"] = domain.Host{ID: "h-1", Name: "Host", NotificationKey: "notif", HostSecret: "hs", WebhookSecret: "ws"}
	reg.HostPolicies["h-1"] = domain.FeePolicy{Type: domain.FeeTypePercent, Value: 2, Currency: "IDR"}
	reg.Products["p-1"] = domain.Product{ID: "p-1", HostID: "h-1", Name: "Prod", Price: 10000}
	reg.HostProviderAccts["h-1"] = []domain.HostProviderAccount{{HostID: "h-1", Provider: "xendit", Env: "sandbox", CredentialsHash: "secret"}}

	mux := SetupMux(orchestration.NewOrchestrator(reg))
	body, _ := json.Marshal(map[string]string{"host_id": "h-1", "product_id": "p-1", "env": "sandbox"})
	req := httptest.NewRequest(http.MethodPost, "/payments", bytes.NewReader(body))
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("expected status 200, got %d body=%s", rec.Code, rec.Body.String())
	}
	var resp createPaymentResponse
	if err := json.NewDecoder(rec.Body).Decode(&resp); err != nil {
		t.Fatalf("decode response failed: %v", err)
	}
	if resp.Reference == "" {
		t.Fatal("reference should be set")
	}

	req2 := httptest.NewRequest(http.MethodGet, "/payments/"+resp.Reference, nil)
	rec2 := httptest.NewRecorder()
	mux.ServeHTTP(rec2, req2)
	if rec2.Code != http.StatusOK {
		t.Fatalf("expected status 200 get, got %d", rec2.Code)
	}
}

func TestWebhookHTTP(t *testing.T) {
	reg := orchestration.NewRegistry()
	reg.Hosts["h-1"] = domain.Host{ID: "h-1", Name: "Host", NotificationKey: "notif", HostSecret: "hs", WebhookSecret: "ws"}
	reg.HostPolicies["h-1"] = domain.FeePolicy{Type: domain.FeeTypePercent, Value: 2, Currency: "IDR"}
	reg.Products["p-1"] = domain.Product{ID: "p-1", HostID: "h-1", Name: "Prod", Price: 10000}
	reg.HostProviderAccts["h-1"] = []domain.HostProviderAccount{{HostID: "h-1", Provider: "midtrans", Env: "sandbox", CredentialsHash: "secret"}}
	orchestrator := orchestration.NewOrchestrator(reg)
	mux := SetupMux(orchestrator)

	createBody, _ := json.Marshal(map[string]string{"host_id": "h-1", "product_id": "p-1", "env": "sandbox"})
	createReq := httptest.NewRequest(http.MethodPost, "/payments", bytes.NewReader(createBody))
	createRec := httptest.NewRecorder()
	mux.ServeHTTP(createRec, createReq)
	var createResp createPaymentResponse
	_ = json.NewDecoder(createRec.Body).Decode(&createResp)

	wh := map[string]string{"reference": createResp.Reference, "status": string(domain.PaymentOrderStatusSuccess), "idempotency_key": "idem-1"}
	whBody, _ := json.Marshal(wh)
	whReq := httptest.NewRequest(http.MethodPost, "/webhooks/midtrans", bytes.NewReader(whBody))
	whRec := httptest.NewRecorder()
	mux.ServeHTTP(whRec, whReq)
	if whRec.Code != http.StatusOK {
		t.Fatalf("expected webhook 200, got %d", whRec.Code)
	}
}
