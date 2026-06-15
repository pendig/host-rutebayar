package proxy

import (
	"context"
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"
)

// OpenAPIProxy maps host-scoped routes ke upstream rute-bayar kontrak.
type OpenAPIProxy struct {
	UpstreamBaseURL string
	Client          *http.Client
}

const maxCreateBodyBytes int64 = 1 << 20

func NewOpenAPIProxy(upstreamBaseURL string) *OpenAPIProxy {
	return &OpenAPIProxy{
		UpstreamBaseURL: strings.TrimRight(upstreamBaseURL, "/"),
		Client:          &http.Client{Timeout: 5 * time.Second},
	}
}

// WebhookReplay enriches upstream event payload for host callback fanout.
type WebhookReplay struct {
	Reference  string `json:"reference"`
	Status     string `json:"status"`
	HostID     string `json:"host_id"`
	Gross      int64  `json:"gross_amount"`
	HostFee    int64  `json:"host_fee_amount"`
	NetAmount  int64  `json:"net_amount"`
	PolicyHash string `json:"policy_hash"`
}

func (p *OpenAPIProxy) upstreamURL(path string) string {
	return fmt.Sprintf("%s%s", p.UpstreamBaseURL, path)
}

func (p *OpenAPIProxy) createPayment(ctx context.Context, hostID string, body []byte) (*http.Response, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, p.upstreamURL("/api/v1/payments"), bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	req.Header.Set("X-Host-ID", hostID)
	req.Header.Set("Content-Type", "application/json")
	return p.Client.Do(req)
}

func (p *OpenAPIProxy) getPaymentStatus(ctx context.Context, hostID, reference string) (*http.Response, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, p.upstreamURL(fmt.Sprintf("/api/v1/payments/%s", reference)), nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("X-Host-ID", hostID)
	return p.Client.Do(req)
}

// ServeHTTP maps host routes:
// POST /host/{id}/payments -> POST /api/v1/payments
// GET  /host/{id}/payments/{ref} -> GET /api/v1/payments/{ref}
func (p *OpenAPIProxy) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if !strings.HasPrefix(r.URL.Path, "/host/") {
		http.Error(w, "invalid path", http.StatusNotFound)
		return
	}
	rest := strings.TrimPrefix(r.URL.Path, "/host/")
	parts := strings.Split(rest, "/")
	if len(parts) < 2 {
		http.Error(w, "invalid host route", http.StatusNotFound)
		return
	}
	hostID := parts[0]
	if hostID == "" {
		http.Error(w, "invalid host route", http.StatusNotFound)
		return
	}

	switch {
	case r.Method == http.MethodPost && len(parts) == 2 && parts[1] == "payments":
		body, err := readAll(r)
		if err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		resp, err := p.createPayment(r.Context(), hostID, body)
		if err != nil {
			http.Error(w, err.Error(), http.StatusBadGateway)
			return
		}
		defer resp.Body.Close()
		if ct := resp.Header.Get("Content-Type"); ct != "" {
			w.Header().Set("Content-Type", ct)
		}
		w.WriteHeader(resp.StatusCode)
		_, _ = io.Copy(w, resp.Body)
	case r.Method == http.MethodGet && len(parts) == 3 && parts[1] == "payments":
		if parts[2] == "" {
			http.Error(w, "invalid host route", http.StatusNotFound)
			return
		}
		resp, err := p.getPaymentStatus(r.Context(), hostID, parts[2])
		if err != nil {
			http.Error(w, err.Error(), http.StatusBadGateway)
			return
		}
		defer resp.Body.Close()
		if ct := resp.Header.Get("Content-Type"); ct != "" {
			w.Header().Set("Content-Type", ct)
		}
		w.WriteHeader(resp.StatusCode)
		_, _ = io.Copy(w, resp.Body)
	default:
		http.Error(w, "unsupported host route", http.StatusNotFound)
	}
}

func (p *OpenAPIProxy) ReplayWebhookFromProvider(payload json.RawMessage) ([]byte, error) {
	var generic struct {
		Reference  string `json:"reference"`
		Status     string `json:"status"`
		HostID     string `json:"host_id"`
		Gross      int64  `json:"gross_amount"`
		HostFee    int64  `json:"host_fee_amount"`
		NetAmount  int64  `json:"net_amount"`
		PolicyHash string `json:"policy_hash"`
	}
	if err := json.Unmarshal(payload, &generic); err != nil {
		return nil, err
	}
	replay := WebhookReplay{
		Reference:  generic.Reference,
		Status:     generic.Status,
		HostID:     generic.HostID,
		Gross:      generic.Gross,
		HostFee:    generic.HostFee,
		NetAmount:  generic.NetAmount,
		PolicyHash: generic.PolicyHash,
	}
	return json.Marshal(replay)
}

func readAll(r *http.Request) ([]byte, error) {
	body, err := io.ReadAll(io.LimitReader(r.Body, maxCreateBodyBytes+1))
	if err != nil {
		return nil, err
	}
	if int64(len(body)) > maxCreateBodyBytes {
		return nil, fmt.Errorf("request body too large")
	}
	return body, nil
}
