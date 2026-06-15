package sdk

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net/http"
)

// Client mirrors minimal host-rutebayar API for website integrations.
type Client struct {
	BaseURL    string
	HTTPClient *http.Client
}

func New(baseURL string) *Client {
	return &Client{BaseURL: baseURL, HTTPClient: &http.Client{}}
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

func (c *Client) CreatePayment(req CreatePaymentRequest) (CreatePaymentResponse, error) {
	payload, err := json.Marshal(req)
	if err != nil {
		return CreatePaymentResponse{}, err
	}
	resp, err := c.HTTPClient.Post(c.BaseURL+"/host/"+req.HostID+"/payments", "application/json", bytes.NewReader(payload))
	if err != nil {
		return CreatePaymentResponse{}, err
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 300 {
		return CreatePaymentResponse{}, fmt.Errorf("unexpected status: %d", resp.StatusCode)
	}
	var out CreatePaymentResponse
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		return CreatePaymentResponse{}, err
	}
	return out, nil
}

func (c *Client) GetPayment(reference string) (PaymentStatus, error) {
	// host-scoped endpoint via proxy route helper
	resp, err := c.HTTPClient.Get(c.BaseURL + "/host/:host-id/payments/" + reference)
	if err != nil {
		return PaymentStatus{}, err
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 300 {
		return PaymentStatus{}, fmt.Errorf("unexpected status: %d", resp.StatusCode)
	}
	var status PaymentStatus
	if err := json.NewDecoder(resp.Body).Decode(&status); err != nil {
		return PaymentStatus{}, err
	}
	return status, nil
}

