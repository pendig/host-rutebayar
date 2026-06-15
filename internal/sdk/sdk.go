package sdk

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"time"
)

// Client mirrors minimal host-rutebayar API for website integrations.
type Client struct {
	BaseURL    string
	HTTPClient *http.Client
}

func New(baseURL string) *Client {
	return &Client{BaseURL: baseURL, HTTPClient: &http.Client{Timeout: 30 * time.Second}}
}

type CreatePaymentRequest struct {
	HostID    string `json:"host_id"`
	ProductID string `json:"product_id"`
	BuyerRef  string `json:"buyer_ref"`
	Env       string `json:"env"`
}

type CreatePaymentResponse struct {
	Reference string `json:"reference"`
	Status    string `json:"status"`
}

type PaymentStatus struct {
	Reference string `json:"reference"`
	Status    string `json:"status"`
}

func (c *Client) CreatePayment(ctx context.Context, req CreatePaymentRequest) (CreatePaymentResponse, error) {
	payload, err := json.Marshal(req)
	if err != nil {
		return CreatePaymentResponse{}, err
	}
	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, c.BaseURL+"/host/"+req.HostID+"/payments", bytes.NewReader(payload))
	if err != nil {
		return CreatePaymentResponse{}, err
	}
	httpReq.Header.Set("Content-Type", "application/json")
	resp, err := c.HTTPClient.Do(httpReq)
	if err != nil {
		return CreatePaymentResponse{}, err
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode >= 300 {
		return CreatePaymentResponse{}, fmt.Errorf("unexpected status: %d", resp.StatusCode)
	}
	var out CreatePaymentResponse
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		return CreatePaymentResponse{}, err
	}
	return out, nil
}

func (c *Client) GetPayment(ctx context.Context, hostID, reference string) (PaymentStatus, error) {
	// host-scoped endpoint via proxy route helper
	httpReq, err := http.NewRequestWithContext(ctx, http.MethodGet, c.BaseURL+"/host/"+hostID+"/payments/"+reference, nil)
	if err != nil {
		return PaymentStatus{}, err
	}
	resp, err := c.HTTPClient.Do(httpReq)
	if err != nil {
		return PaymentStatus{}, err
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode >= 300 {
		return PaymentStatus{}, fmt.Errorf("unexpected status: %d", resp.StatusCode)
	}
	var status PaymentStatus
	if err := json.NewDecoder(resp.Body).Decode(&status); err != nil {
		return PaymentStatus{}, err
	}
	return status, nil
}
