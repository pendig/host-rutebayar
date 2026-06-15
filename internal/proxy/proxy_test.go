package proxy

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
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
		if ct := r.Header.Get("Content-Type"); ct != "application/json" {
			t.Fatalf("unexpected content type: %s", ct)
		}
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusCreated)
		_, _ = w.Write([]byte(`{"reference":"ord-1","status":"created"}`))
	}))
	defer upstream.Close()

	reqBody, _ := json.Marshal(map[string]string{"product_id": "p-1"})
	proxy := NewOpenAPIProxy(upstream.URL)
	req := httptest.NewRequestWithContext(context.Background(), http.MethodPost, "/host/host-1/payments", bytes.NewReader(reqBody))
	rec := httptest.NewRecorder()
	proxy.ServeHTTP(rec, req)
	if rec.Code != http.StatusCreated {
		t.Fatalf("expected created, got %d", rec.Code)
	}
	var payload map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &payload); err != nil {
		t.Fatalf("invalid payload: %v", err)
	}
	if payload["reference"] != "ord-1" || payload["status"] != "created" {
		t.Fatalf("unexpected payload: %v", payload)
	}
	if rec.Header().Get("Content-Type") != "application/json" {
		t.Fatalf("expected content-type passthrough, got %s", rec.Header().Get("Content-Type"))
	}
}

func TestHostRouteGetForward(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet || r.URL.Path != "/api/v1/payments/ref-1" {
			t.Fatalf("unexpected upstream request %s %s", r.Method, r.URL.Path)
		}
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"reference":"ref-1","status":"success"}`))
	}))
	defer upstream.Close()

	proxy := NewOpenAPIProxy(upstream.URL)
	req := httptest.NewRequestWithContext(context.Background(), http.MethodGet, "/host/host-1/payments/ref-1", nil)
	rec := httptest.NewRecorder()
	proxy.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", rec.Code)
	}
	if rec.Header().Get("Content-Type") != "application/json" {
		t.Fatalf("expected content-type passthrough, got %s", rec.Header().Get("Content-Type"))
	}
	var payload map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &payload); err != nil {
		t.Fatalf("invalid payload: %v", err)
	}
	if payload["reference"] != "ref-1" || payload["status"] != "success" {
		t.Fatalf("unexpected payload: %v", payload)
	}
}

func TestHostRouteInvalidPathRejectsAndDoesNotForward(t *testing.T) {
	called := false
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		called = true
		t.Fatalf("unexpected upstream call: %s %s", r.Method, r.URL.Path)
	}))
	defer upstream.Close()

	proxy := NewOpenAPIProxy(upstream.URL)

	for _, tt := range []struct {
		path string
		method string
	}{
		{path: "/host//payments", method: http.MethodPost},
		{path: "/host/host-1/payments/", method: http.MethodGet},
	} {
		req := httptest.NewRequestWithContext(context.Background(), tt.method, tt.path, strings.NewReader("{}"))
		rec := httptest.NewRecorder()
		proxy.ServeHTTP(rec, req)
		if rec.Code != http.StatusNotFound {
			t.Fatalf("expected 404 for %s, got %d", tt.path, rec.Code)
		}
	}

	if called {
		t.Fatalf("upstream was called for malformed host route")
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
