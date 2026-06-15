package proxy

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestHostRouteCreateAndForward(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost || r.URL.Path != "/api/v1/payments" {
			t.Fatalf("unexpected upstream request %s %s", r.Method, r.URL.Path)
		}
		if r.Header.Get("X-Host-ID") != "host-1" {
			t.Fatalf("missing host header")
		}
		w.WriteHeader(http.StatusCreated)
		_, _ = w.Write([]byte(`{"reference":"ord-1","status":"created"}`))
	}))
	defer upstream.Close()

	reqBody, _ := json.Marshal(map[string]string{"product_id": "p-1"})
	proxy := NewOpenAPIProxy(upstream.URL)
	req := httptest.NewRequest(http.MethodPost, "/host/host-1/payments", bytes.NewReader(reqBody))
	rec := httptest.NewRecorder()
	proxy.ServeHTTP(rec, req)
	if rec.Code != http.StatusCreated {
		t.Fatalf("expected created, got %d", rec.Code)
	}
}

func TestHostRouteGetForward(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet || r.URL.Path != "/api/v1/payments/ref-1" {
			t.Fatalf("unexpected upstream request %s %s", r.Method, r.URL.Path)
		}
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"reference":"ref-1","status":"success"}`))
	}))
	defer upstream.Close()

	proxy := NewOpenAPIProxy(upstream.URL)
	req := httptest.NewRequest(http.MethodGet, "/host/host-1/payments/ref-1", nil)
	rec := httptest.NewRecorder()
	proxy.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", rec.Code)
	}
}

func TestReplayWebhookMapping(t *testing.T) {
	p := NewOpenAPIProxy("http://localhost")
	raw, _ := json.Marshal(map[string]any{"reference": "ref-1", "status": "success", "host_id": "host-1", "gross_amount": 100000, "host_fee_amount": 1000, "net_amount": 98000, "policy_hash": "abc"})
	mapped, err := p.ReplayWebhookFromProvider(raw)
	if err != nil {
		t.Fatalf("unexpected err: %v", err)
	}
	var out WebhookReplay
	if err := json.Unmarshal(mapped, &out); err != nil {
		t.Fatalf("invalid mapped payload: %v", err)
	}
	if out.Reference != "ref-1" || out.HostID != "host-1" {
		t.Fatalf("unexpected mapping: %+v", out)
	}
}
