package orchestration

import (
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"math"
	"sync"
	"time"

	"github.com/pendig/host-rutebayar/internal/domain"
)

// Registry stores runtime catalogs used by orchestrator.
type Registry struct {
	Hosts             map[string]domain.Host
	Products          map[string]domain.Product
	HostPolicies      map[string]domain.FeePolicy
	HostProviderAccts map[string][]domain.HostProviderAccount
}

func NewRegistry() *Registry {
	return &Registry{
		Hosts:             map[string]domain.Host{},
		Products:          map[string]domain.Product{},
		HostPolicies:      map[string]domain.FeePolicy{},
		HostProviderAccts: map[string][]domain.HostProviderAccount{},
	}
}

// Orchestrator executes payment lifecycle in-memory for phase 2.
type Orchestrator struct {
	registry     *Registry
	orders       map[string]domain.PaymentOrder
	ledgers      map[string]domain.PaymentOrderLedger
	idempotent   map[string]bool
	providerFee  float64
	mu           sync.Mutex
}

// NewOrchestrator creates a default service with 2.5% provider fee.
func NewOrchestrator(registry *Registry) *Orchestrator {
	return &Orchestrator{
		registry:    registry,
		orders:      map[string]domain.PaymentOrder{},
		ledgers:     map[string]domain.PaymentOrderLedger{},
		idempotent:  map[string]bool{},
		providerFee: 2.5,
	}
}

// CreateInput defines payload for internal payment creation.
type CreateInput struct {
	HostID    string
	ProductID string
	BuyerRef  string
	Env       string
}

// CreateOutput exposes response shape for API layer and tests.
type CreateOutput struct {
	Reference string
	Status    domain.PaymentOrderStatus
	Order     domain.PaymentOrder
}

// CreatePayment creates order snapshot and stores immutable fee lines.
func (s *Orchestrator) CreatePayment(in CreateInput) (CreateOutput, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	host, ok := s.registry.Hosts[in.HostID]
	if !ok {
		return CreateOutput{}, errors.New("host not found")
	}
	if err := host.Validate(); err != nil {
		return CreateOutput{}, fmt.Errorf("invalid host: %w", err)
	}

	product, ok := s.registry.Products[in.ProductID]
	if !ok {
		return CreateOutput{}, errors.New("product not found")
	}
	if err := product.Validate(); err != nil {
		return CreateOutput{}, fmt.Errorf("invalid product: %w", err)
	}
	if product.HostID != host.ID {
		return CreateOutput{}, errors.New("product does not belong to host")
	}

	policy := s.registry.HostPolicies[host.ID]
	if product.FeePolicyOverride != nil {
		policy = *product.FeePolicyOverride
	}
	hostFee, err := policy.CalculateHostFee(product.Price)
	if err != nil {
		return CreateOutput{}, err
	}
	providerFee := int64(math.Round(float64(product.Price) * (s.providerFee / 100)))
	if providerFee < 0 {
		providerFee = 0
	}
	netAmount := product.Price - hostFee - providerFee
	if netAmount < 0 {
		return CreateOutput{}, errors.New("policy produces invalid settlement")
	}

	if _, ok := s.registry.HostProviderAccts[host.ID]; !ok {
		return CreateOutput{}, errors.New("provider account not configured")
	}
	env := in.Env
	if env == "" {
		env = "sandbox"
	}
	provider := selectProvider(in.HostID, env, s.registry)
	if provider == "" {
		return CreateOutput{}, errors.New("provider not available")
	}

	reference := newReference()
	snapshot := domain.NewFeePolicySnapshot(policy)
	order := domain.PaymentOrder{
		ID:               reference,
		Reference:        reference,
		HostID:           host.ID,
		ProductID:        product.ID,
		Provider:         provider,
		Currency:         policy.Currency,
		Env:              env,
		Status:           domain.PaymentOrderStatusCreated,
		GrossAmount:      product.Price,
		HostFeeAmount:    hostFee,
		ProviderFeeAmount: providerFee,
		NetAmount:        netAmount,
		BuyerRef:         in.BuyerRef,
		PolicySnapshotID:  snapshot.PolicyID,
	}
	ledger := domain.PaymentOrderLedger{
		PaymentOrderID:    reference,
		GrossAmount:       product.Price,
		HostFeeAmount:     hostFee,
		ProviderFeeAmount: providerFee,
		NetAmount:         netAmount,
		PolicyChecksum:    snapshot.PolicyPayloadHash,
		IdempotencyKey:    fmt.Sprintf("policy:%s|%s", snapshot.PolicyID, reference),
	}

	s.orders[reference] = order
	s.ledgers[reference] = ledger
	return CreateOutput{Reference: reference, Status: order.Status, Order: order}, nil
}

// GetPayment returns payment by reference.
func (s *Orchestrator) GetPayment(reference string) (domain.PaymentOrder, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	order, ok := s.orders[reference]
	if !ok {
		return domain.PaymentOrder{}, errors.New("payment not found")
	}
	return order, nil
}

// GetLedger returns settlement ledger.
func (s *Orchestrator) GetLedger(reference string) (domain.PaymentOrderLedger, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	ledger, ok := s.ledgers[reference]
	if !ok {
		return domain.PaymentOrderLedger{}, errors.New("ledger not found")
	}
	return ledger, nil
}

// ReconcileWebhook idempotently updates order status.
func (s *Orchestrator) ReconcileWebhook(reference, provider, status, idempotencyKey string) (domain.PaymentOrderStatus, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	order, ok := s.orders[reference]
	if !ok {
		return "", errors.New("payment not found")
	}
	if order.Provider != provider {
		return "", errors.New("provider mismatch")
	}
	if s.idempotent[idempotencyKey] {
		return order.Status, nil
	}
	next := domain.PaymentOrderStatus(status)
	switch next {
	case domain.PaymentOrderStatusSuccess, domain.PaymentOrderStatusFailed, domain.PaymentOrderStatusExpired:
		order.Status = next
	default:
		return "", fmt.Errorf("unsupported status: %s", status)
	}
	s.orders[reference] = order
	s.idempotent[idempotencyKey] = true
	return next, nil
}

func selectProvider(hostID, env string, reg *Registry) string {
	for _, acct := range reg.HostProviderAccts[hostID] {
		if acct.Env == env {
			return acct.Provider
		}
	}
	return ""
}

func newReference() string {
	buf := make([]byte, 8)
	_, _ = rand.Read(buf)
	return fmt.Sprintf("ord-%d-%s", time.Now().UnixNano(), hex.EncodeToString(buf))
}
