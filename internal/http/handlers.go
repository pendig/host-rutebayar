package httphandlers

import (
	"encoding/json"
	"fmt"
	"net/http"
	"strings"

	"github.com/pendig/host-rutebayar/internal/orchestration"
)

// CreatePaymentRequest is payload for POST /payments.
type CreatePaymentRequest struct {
	HostID    string `json:"host_id"`
	ProductID string `json:"product_id"`
	BuyerRef  string `json:"buyer_ref"`
	Env       string `json:"env"`
}

type createPaymentResponse struct {
	Reference string `json:"reference"`
	Status    string `json:"status"`
}

// PaymentStatusResponse is returned by GET /payments/{reference}.
type PaymentStatusResponse struct {
	Reference string `json:"reference"`
	Status    string `json:"status"`
	HostFee   int64  `json:"host_fee_amount"`
	Gross     int64  `json:"gross_amount"`
	Net       int64  `json:"net_amount"`
}

type webhookPayload struct {
	Reference      string `json:"reference"`
	Status         string `json:"status"`
	IdempotencyKey string `json:"idempotency_key"`
}

func SetupMux(orchestrator *orchestration.Orchestrator) *http.ServeMux {
	mux := http.NewServeMux()
	mux.HandleFunc("/payments", func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodPost {
			handleCreatePayment(w, r, orchestrator)
			return
		}
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
	})
	mux.HandleFunc("/payments/", func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodGet {
			handleGetPayment(w, r, orchestrator)
			return
		}
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
	})
	mux.HandleFunc("/webhooks/", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		handleWebhook(w, r, orchestrator)
	})
	return mux
}

func handleCreatePayment(w http.ResponseWriter, r *http.Request, orchestrator *orchestration.Orchestrator) {
	var req CreatePaymentRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, fmt.Sprintf("invalid body: %v", err), http.StatusBadRequest)
		return
	}
	if req.HostID == "" || req.ProductID == "" {
		http.Error(w, "host_id and product_id are required", http.StatusBadRequest)
		return
	}
	out, err := orchestrator.CreatePayment(orchestration.CreateInput{
		HostID:    req.HostID,
		ProductID: req.ProductID,
		BuyerRef:  req.BuyerRef,
		Env:       req.Env,
	})
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	response := createPaymentResponse{Reference: out.Reference, Status: string(out.Order.Status)}
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(response)
}

func handleGetPayment(w http.ResponseWriter, r *http.Request, orchestrator *orchestration.Orchestrator) {
	reference := strings.TrimPrefix(r.URL.Path, "/payments/")
	if reference == "" {
		http.Error(w, "reference is required", http.StatusBadRequest)
		return
	}
	order, err := orchestrator.GetPayment(reference)
	if err != nil {
		http.Error(w, err.Error(), http.StatusNotFound)
		return
	}
	response := PaymentStatusResponse{Reference: order.Reference, Status: string(order.Status), HostFee: order.HostFeeAmount, Gross: order.GrossAmount, Net: order.NetAmount}
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(response)
}

func handleWebhook(w http.ResponseWriter, r *http.Request, orchestrator *orchestration.Orchestrator) {
	provider := strings.TrimPrefix(r.URL.Path, "/webhooks/")
	if provider == "" {
		http.Error(w, "provider is required", http.StatusBadRequest)
		return
	}
	var payload webhookPayload
	if err := json.NewDecoder(r.Body).Decode(&payload); err != nil {
		http.Error(w, "invalid body", http.StatusBadRequest)
		return
	}
	if payload.Reference == "" || payload.Status == "" || payload.IdempotencyKey == "" {
		http.Error(w, "reference, status, and idempotency_key are required", http.StatusBadRequest)
		return
	}
	status, err := orchestrator.ReconcileWebhook(payload.Reference, provider, payload.Status, payload.IdempotencyKey)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(map[string]string{"status": string(status)})
}
