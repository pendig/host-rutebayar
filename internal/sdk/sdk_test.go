package sdk

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestSDKCreatePayment(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			t.Fatalf("expected POST, got %s", r.Method)
		}
		if !strings.HasPrefix(r.URL.Path, "/host/host-1/payments") {
			t.Fatalf("unexpected path: %s", r.URL.Path)
		}
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"reference":"r1","status":"created"}`))
	}))
	defer server.Close()

	client := New(server.URL)
	resp, err := client.CreatePayment(CreatePaymentRequest{HostID: "host-1", ProductID: "p-1"})
	if err != nil {
		t.Fatalf("create failed: %v", err)
	}
	if resp.Reference != "r1" {
		t.Fatalf("unexpected ref %s", resp.Reference)
	}
}

func TestSDKGetPayment(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			t.Fatalf("expected GET, got %s", r.Method)
		}
		w.WriteHeader(http.StatusOK)
		_ = json.NewEncoder(w).Encode(PaymentStatus{Reference: "r1", Status: "success"})
	}))
	defer server.Close()

	client := New(server.URL)
	_, err := client.GetPayment("r1")
	if err != nil {
		t.Fatalf("get failed: %v", err)
	}
}
