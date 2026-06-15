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
	"github.com/pendig/host-rutebayar/internal/observability"
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

// Orchestrator executes payment lifecycle in-memory for phase 2/4.
type Orchestrator struct {
	registry   *Registry
	orders     map[string]domain.PaymentOrder
	ledgers    map[string]domain.PaymentOrderLedger
	idempotent map[string]bool
	providerFee float64
	mu         sync.Mutex

	metrics      *observability.Collector
	audit        *observability.AuditTrail
	retryPolicy  observability.RetryPolicy
	deadLetters  *observability.DeadLetterQueue
}

// NewOrchestrator creates default service with 2.5% provider fee.
func NewOrchestrator(registry *Registry) *Orchestrator {
	return NewOrchestratorWithDependencies(registry, observability.NewCollector(), observability.NewAuditTrail(), observability.DefaultRetryPolicy(), observability.NewDeadLetterQueue())
}

func NewOrchestratorWithDependencies(
	registry *Registry,
	collector *observability.Collector,
	audit *observability.AuditTrail,
	retry observability.RetryPolicy,
	dlq *observability.DeadLetterQueue,
) *Orchestrator {
	return &Orchestrator{
		registry:    registry,
		orders:      map[string]domain.PaymentOrder{},
		ledgers:     map[string]domain.PaymentOrderLedger{},
		idempotent:  map[string]bool{},
		providerFee: 2.5,
		metrics:     collector,
		audit:       audit,
		retryPolicy: retry,
		deadLetters: dlq,
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
	s.metrics.Inc("payments.create")

	host, ok := s.registry.Hosts[in.HostID]
	if !ok {
		s.metrics.Inc("payments.create.error.host_not_found")
		return CreateOutput{}, errors.New("host not found")
	}
	if err := host.Validate(); err != nil {
		s.metrics.Inc("payments.create.error.host_invalid")
		return CreateOutput{}, fmt.Errorf("invalid host: %w", err)
	}

	product, ok := s.registry.Products[in.ProductID]
	if !ok {
		s.metrics.Inc("payments.create.error.product_not_found")
		return CreateOutput{}, errors.New("product not found")
	}
	if err := product.Validate(); err != nil {
		s.metrics.Inc("payments.create.error.product_invalid")
		return CreateOutput{}, fmt.Errorf("invalid product: %w", err)
	}
	if product.HostID != host.ID {
		s.metrics.Inc("payments.create.error.product_mismatch")
		return CreateOutput{}, errors.New("product does not belong to host")
	}

	policy := s.registry.HostPolicies[host.ID]
	if product.FeePolicyOverride != nil {
		policy = *product.FeePolicyOverride
	}
	hostFee, err := policy.CalculateHostFee(product.Price)
	if err != nil {
		s.metrics.Inc("payments.create.error.fee")
		return CreateOutput{}, err
	}
	providerFee := int64(math.Round(float64(product.Price) * (s.providerFee / 100)))
	if providerFee < 0 {
		providerFee = 0
	}
	netAmount := product.Price - hostFee - providerFee
	if netAmount < 0 {
		s.metrics.Inc("payments.create.error.settlement")
		return CreateOutput{}, errors.New("policy produces invalid settlement")
	}

	if _, ok := s.registry.HostProviderAccts[host.ID]; !ok {
		s.metrics.Inc("payments.create.error.provider_missing")
		return CreateOutput{}, errors.New("provider account not configured")
	}
	env := in.Env
	if env == "" {
		env = "sandbox"
	}
	provider := selectProvider(in.HostID, env, s.registry)
	if provider == "" {
		s.metrics.Inc("payments.create.error.provider_missing")
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
	s.audit.Append("payment_created", reference, map[string]string{"host": host.ID, "provider": provider})

	return CreateOutput{Reference: reference, Status: order.Status, Order: order}, nil
}

// GetPayment returns payment by reference.
func (s *Orchestrator) GetPayment(reference string) (domain.PaymentOrder, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.metrics.Inc("payments.get")
	order, ok := s.orders[reference]
	if !ok {
		s.metrics.Inc("payments.get.not_found")
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
	s.metrics.Inc("payments.webhook")

	order, ok := s.orders[reference]
	if !ok {
		s.metrics.Inc("payments.webhook.not_found")
		return "", errors.New("payment not found")
	}
	if order.Provider != provider {
		s.metrics.Inc("payments.webhook.provider_mismatch")
		return "", errors.New("provider mismatch")
	}
	if s.idempotent[idempotencyKey] {
		s.metrics.Inc("payments.webhook.idempotent")
		return order.Status, nil
	}
	next := domain.PaymentOrderStatus(status)
	switch next {
	case domain.PaymentOrderStatusSuccess, domain.PaymentOrderStatusFailed, domain.PaymentOrderStatusExpired:
		order.Status = next
	default:
		s.metrics.Inc("payments.webhook.invalid_status")
		s.deadLetters.Push(observability.DeadLetterItem{
			At:        time.Now().UTC(),
			Reference: reference,
			Provider:  provider,
			Reason:    "invalid_status",
			Payload:   status,
		})
		return "", fmt.Errorf("unsupported status: %s", status)
	}

	s.orders[reference] = order
	s.idempotent[idempotencyKey] = true
	s.audit.Append("payment_webhook", reference, map[string]string{"provider": provider, "status": status})
	s.metrics.Inc("payments.webhook.success")
	return next, nil
}

// ReconcileWebhookWithRetry applies retry policy to webhook reconciliation simulation.
func (s *Orchestrator) ReconcileWebhookWithRetry(reference, provider, status, idempotencyKey string) (domain.PaymentOrderStatus, error) {
	var statusResp domain.PaymentOrderStatus
	var err error
	for attempt := 1; attempt <= s.retryPolicy.MaxAttempts; attempt++ {
		statusResp, err = s.ReconcileWebhook(reference, provider, status, idempotencyKey)
		if err == nil {
			return statusResp, nil
		}
		if attempt == s.retryPolicy.MaxAttempts {
			return "", err
		}
		s.metrics.Inc("payments.webhook.retry")
		delay := s.retryPolicy.DelayForAttempt(attempt)
		time.Sleep(delay)
	}
	return statusResp, err
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

func (s *Orchestrator) Collector() *observability.Collector {
	return s.metrics
}

func (s *Orchestrator) AuditTrail() *observability.AuditTrail {
	return s.audit
}

func (s *Orchestrator) DeadLetters() *observability.DeadLetterQueue {
	return s.deadLetters
}
