package gateway

import (
	"context"
	"fmt"
	"time"
)

// CreateInvoiceInput is input for provider adapter.
type CreateInvoiceInput struct {
	Reference   string
	HostID      string
	ProductID   string
	Provider    string
	Environment string
	Amount      int64
	Currency    string
}

// InvoiceResult is the provider return payload.
type InvoiceResult struct {
	Reference      string
	CheckoutURL    string
	ProviderRef    string
	ProviderStatus string
}

// ProviderGateway abstracts gateway adapter integration.
type ProviderGateway interface {
	CreateInvoice(ctx context.Context, input CreateInvoiceInput) (InvoiceResult, error)
}

// DummyGateway is a deterministic mock gateway implementation.
type DummyGateway struct{}

func (d DummyGateway) CreateInvoice(_ context.Context, input CreateInvoiceInput) (InvoiceResult, error) {
	if input.Reference == "" {
		return InvoiceResult{}, ErrMissingReference
	}
	if input.Provider == "" {
		return InvoiceResult{}, ErrMissingProvider
	}
	return InvoiceResult{
		Reference:      input.Reference,
		CheckoutURL:    fmt.Sprintf("https://provider.%s/%s/checkout", input.Provider, input.Reference),
		ProviderRef:    fmt.Sprintf("prov-%s", input.Reference),
		ProviderStatus: "created",
	}, nil
}

// ErrMissingReference indicates invalid provider input.
var ErrMissingReference = fmt.Errorf("provider input missing reference")

// ErrMissingProvider indicates invalid provider.
var ErrMissingProvider = fmt.Errorf("provider input missing provider")

// DefaultGateway returns a mock implementation for phase 6.
func DefaultGateway() ProviderGateway {
	return DummyGateway{}
}

// CheckoutTimeout is a tiny guard for future provider retry support.
const CheckoutTimeout = 5 * time.Second
