package orchestration

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"math"
	"strings"
	"sync"
	"time"

	"github.com/pendig/host-rutebayar/internal/domain"
	"github.com/pendig/host-rutebayar/internal/gateway"
	"github.com/pendig/host-rutebayar/internal/observability"
)

// Store defines optional persistence operations.
type Store interface {
	UpsertHost(host domain.Host) error
	GetHost(hostID string) (domain.Host, error)
	ListHosts() ([]domain.Host, error)
	UpsertHostPolicy(hostID string, policy domain.FeePolicy) error
	GetHostPolicy(hostID string) (domain.FeePolicy, error)
	UpsertProduct(product domain.Product) error
	GetProduct(productID string) (domain.Product, error)
	ListProducts() ([]domain.Product, error)
	UpsertProviderAccount(account domain.HostProviderAccount) error
	GetProviderAccounts(hostID string) ([]domain.HostProviderAccount, error)
	SaveOrder(order domain.PaymentOrder, ledger domain.PaymentOrderLedger) error
	GetOrder(reference string) (domain.PaymentOrder, error)
	GetLedger(reference string) (domain.PaymentOrderLedger, error)
	ListOrders() ([]domain.PaymentOrder, error)
}

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

// Orchestrator executes payment lifecycle for registry and sqlite modes.
type Orchestrator struct {
	registry    *Registry
	store       Store
	gateway     gateway.ProviderGateway
	orders      map[string]domain.PaymentOrder
	ledgers     map[string]domain.PaymentOrderLedger
	idempotent  map[string]bool
	providerFee float64
	mu          sync.Mutex

	metrics     *observability.Collector
	audit       *observability.AuditTrail
	retryPolicy observability.RetryPolicy
	deadLetters *observability.DeadLetterQueue
}

// NewOrchestrator creates default service using in-memory registry.
func NewOrchestrator(registry *Registry) *Orchestrator {
	return NewOrchestratorWithDependencies(registry, observability.NewCollector(), observability.NewAuditTrail(), observability.DefaultRetryPolicy(), observability.NewDeadLetterQueue())
}

// NewOrchestratorWithDependencies builds orchestrator with custom observability stack.
func NewOrchestratorWithDependencies(
	registry *Registry,
	collector *observability.Collector,
	audit *observability.AuditTrail,
	retry observability.RetryPolicy,
	dlq *observability.DeadLetterQueue,
) *Orchestrator {
	return &Orchestrator{
		registry:    registry,
		gateway:     gateway.DefaultGateway(),
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

// NewOrchestratorWithStore boots service with sqlite-backed persistence.
func NewOrchestratorWithStore(store Store, provider gateway.ProviderGateway) *Orchestrator {
	orchestrator := NewOrchestrator(NewRegistry())
	orchestrator.store = store
	if provider != nil {
		orchestrator.gateway = provider
	}
	return orchestrator
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

	host, err := s.findHost(in.HostID)
	if err != nil {
		s.metrics.Inc("payments.create.error.host_not_found")
		return CreateOutput{}, err
	}
	if err := host.Validate(); err != nil {
		s.metrics.Inc("payments.create.error.host_invalid")
		return CreateOutput{}, fmt.Errorf("invalid host: %w", err)
	}

	product, err := s.findProduct(in.ProductID)
	if err != nil {
		s.metrics.Inc("payments.create.error.product_not_found")
		return CreateOutput{}, err
	}
	if err := product.Validate(); err != nil {
		s.metrics.Inc("payments.create.error.product_invalid")
		return CreateOutput{}, fmt.Errorf("invalid product: %w", err)
	}
	if !product.IsActive {
		s.metrics.Inc("payments.create.error.product_inactive")
		return CreateOutput{}, errors.New("product is inactive")
	}
	if product.HostID != host.ID {
		s.metrics.Inc("payments.create.error.product_mismatch")
		return CreateOutput{}, errors.New("product does not belong to host")
	}

	policy := s.defaultPolicy()
	if product.FeePolicyOverride != nil {
		policy = *product.FeePolicyOverride
	} else {
		rules, err := s.findHostPolicy(host.ID)
		if err == nil {
			policy = rules
		}
	}
	if policy.Rounding == "" {
		policy.Rounding = domain.RoundingRuleNearest
	}
	if policy.Currency == "" {
		policy.Currency = "IDR"
	}
	if policy.PolicyVersion == "" {
		policy.PolicyVersion = "v1"
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

	env := in.Env
	if env == "" {
		env = "sandbox"
	}
	provider, err := s.selectProvider(host.ID, env)
	if err != nil {
		s.metrics.Inc("payments.create.error.provider_missing")
		return CreateOutput{}, err
	}

	reference := newReference()
	invoice, err := s.gateway.CreateInvoice(context.Background(), gateway.CreateInvoiceInput{
		Reference:   reference,
		HostID:      host.ID,
		ProductID:   product.ID,
		Provider:    provider,
		Environment: env,
		Amount:      product.Price,
		Currency:    policy.Currency,
	})
	if err != nil {
		s.metrics.Inc("payments.create.error.gateway")
		return CreateOutput{}, err
	}

	snapshot := domain.NewFeePolicySnapshot(policy)
	order := domain.PaymentOrder{
		ID:                  reference,
		Reference:           reference,
		HostID:              host.ID,
		ProductID:           product.ID,
		Provider:            provider,
		ProviderReference:   invoice.ProviderRef,
		ProviderCheckoutURL: invoice.CheckoutURL,
		Currency:            policy.Currency,
		Env:                 env,
		Status:              domain.PaymentOrderStatusCreated,
		GrossAmount:         product.Price,
		HostFeeAmount:       hostFee,
		ProviderFeeAmount:   providerFee,
		NetAmount:           netAmount,
		BuyerRef:            in.BuyerRef,
		PolicySnapshotID:    snapshot.PolicyID,
	}
	if invoice.ProviderStatus != "" {
		order.Status = domain.PaymentOrderStatus(invoice.ProviderStatus)
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
	if err := s.saveOrder(order, ledger); err != nil {
		s.metrics.Inc("payments.create.error.store")
		return CreateOutput{}, err
	}

	s.audit.Append("payment_created", reference, map[string]string{"host": host.ID, "provider": provider})
	return CreateOutput{Reference: reference, Status: order.Status, Order: order}, nil
}

// GetPayment returns payment by reference.
func (s *Orchestrator) GetPayment(reference string) (domain.PaymentOrder, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.metrics.Inc("payments.get")
	return s.getOrderLocked(reference)
}

// GetLedger returns settlement ledger.
func (s *Orchestrator) GetLedger(reference string) (domain.PaymentOrderLedger, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.getLedgerLocked(reference)
}

// ReconcileWebhook idempotently updates order status.
func (s *Orchestrator) ReconcileWebhook(reference, provider, status, idempotencyKey string) (domain.PaymentOrderStatus, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.metrics.Inc("payments.webhook")

	order, err := s.getOrderLocked(reference)
	if err != nil {
		s.metrics.Inc("payments.webhook.not_found")
		return "", err
	}
	if order.Provider != provider {
		s.metrics.Inc("payments.webhook.provider_mismatch")
		return "", errors.New("provider mismatch")
	}
	if s.idempotent[webhookIdempotencyKey(reference, provider, idempotencyKey)] {
		s.metrics.Inc("payments.webhook.idempotent")
		return order.Status, nil
	}

	next := domain.PaymentOrderStatus(status)
	switch order.Status {
	case domain.PaymentOrderStatusSuccess, domain.PaymentOrderStatusFailed, domain.PaymentOrderStatusExpired:
		if order.Status == next {
			s.metrics.Inc("payments.webhook.idempotent")
			return order.Status, nil
		}
		s.metrics.Inc("payments.webhook.invalid_transition")
		return "", fmt.Errorf("invalid status transition: %s -> %s", order.Status, next)
	}

	switch next {
	case domain.PaymentOrderStatusSuccess, domain.PaymentOrderStatusFailed, domain.PaymentOrderStatusExpired:
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

	order.Status = next
	ledger, ledgerErr := s.getLedgerLocked(reference)
	if ledgerErr != nil {
		ledger = domain.PaymentOrderLedger{
			PaymentOrderID:    order.Reference,
			GrossAmount:       order.GrossAmount,
			HostFeeAmount:     order.HostFeeAmount,
			ProviderFeeAmount: order.ProviderFeeAmount,
			NetAmount:         order.NetAmount,
			PolicyChecksum:    fmt.Sprintf("policy:%s", order.PolicySnapshotID),
			IdempotencyKey:    idempotencyKey,
		}
	}
	if err := s.saveOrder(order, ledger); err != nil {
		return "", err
	}

	s.idempotent[webhookIdempotencyKey(reference, provider, idempotencyKey)] = true
	s.audit.Append("payment_webhook", reference, map[string]string{"provider": provider, "status": status})
	s.metrics.Inc("payments.webhook.success")
	return next, nil
}

func webhookIdempotencyKey(reference, provider, idempotencyKey string) string {
	return reference + ":" + provider + ":" + idempotencyKey
}

// ReconcileWebhookWithRetry applies retry policy to webhook reconciliation simulation.
func (s *Orchestrator) ReconcileWebhookWithRetry(reference, provider, status, idempotencyKey string) (domain.PaymentOrderStatus, error) {
	statusResp, _, err := s.ReconcileWebhookWithRetryWithAttempts(context.Background(), reference, provider, status, idempotencyKey)
	return statusResp, err
}

func (s *Orchestrator) ReconcileWebhookWithRetryWithAttempts(ctx context.Context, reference, provider, status, idempotencyKey string) (domain.PaymentOrderStatus, int, error) {
	var statusResp domain.PaymentOrderStatus
	var err error
	attempts := 0
	for attempt := 1; attempt <= s.retryPolicy.MaxAttempts; attempt++ {
		if ctx != nil {
			select {
			case <-ctx.Done():
				return "", attempts, ctx.Err()
			default:
			}
		}

		attempts = attempt
		statusResp, err = s.ReconcileWebhook(reference, provider, status, idempotencyKey)
		if err == nil {
			return statusResp, attempts, nil
		}
		if !isRetryableWebhookError(err) {
			return "", attempts, err
		}
		if attempt == s.retryPolicy.MaxAttempts {
			return "", attempts, err
		}
		s.metrics.Inc("payments.webhook.retry")
		delay := s.retryPolicy.DelayForAttempt(attempt)
		if ctx != nil {
			select {
			case <-ctx.Done():
				return "", attempts, ctx.Err()
			case <-time.After(delay):
			}
			continue
		}
		time.Sleep(delay)
	}
	return statusResp, attempts, err
}

func isRetryableWebhookError(err error) bool {
	if err == nil {
		return false
	}
	msg := strings.ToLower(err.Error())
	if strings.Contains(msg, "not found") {
		return false
	}
	if strings.Contains(msg, "no rows") {
		return false
	}
	if strings.Contains(msg, "provider mismatch") {
		return false
	}
	if strings.Contains(msg, "invalid status transition") {
		return false
	}
	if strings.Contains(msg, "unsupported status") {
		return false
	}
	return true
}

// RegisterHost stores host profile in-memory or sqlite.
func (s *Orchestrator) RegisterHost(host domain.Host) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := host.Validate(); err != nil {
		return err
	}
	if s.store != nil {
		return s.store.UpsertHost(host)
	}
	s.registry.Hosts[host.ID] = host
	return nil
}

// RegisterProduct stores product catalog in-memory or sqlite.
func (s *Orchestrator) RegisterProduct(product domain.Product) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := product.Validate(); err != nil {
		return err
	}
	if s.store != nil {
		return s.store.UpsertProduct(product)
	}
	s.registry.Products[product.ID] = product
	return nil
}

// RegisterProviderAccount stores provider account in-memory or sqlite.
func (s *Orchestrator) RegisterProviderAccount(account domain.HostProviderAccount) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := account.Validate(); err != nil {
		return err
	}
	if s.store != nil {
		return s.store.UpsertProviderAccount(account)
	}
	s.registry.HostProviderAccts[account.HostID] = s.filterExistingAccounts(s.registry.HostProviderAccts[account.HostID], account)
	return nil
}

// RegisterHostPolicy stores host default policy.
func (s *Orchestrator) RegisterHostPolicy(hostID string, policy domain.FeePolicy) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := policy.Validate(); err != nil {
		return err
	}
	if hostID == "" {
		return errors.New("host id is required")
	}
	if s.store != nil {
		return s.store.UpsertHostPolicy(hostID, policy)
	}
	s.registry.HostPolicies[hostID] = policy
	return nil
}

func (s *Orchestrator) ListHosts() ([]domain.Host, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.store != nil {
		return s.store.ListHosts()
	}
	hosts := make([]domain.Host, 0, len(s.registry.Hosts))
	for _, host := range s.registry.Hosts {
		hosts = append(hosts, host)
	}
	return hosts, nil
}

func (s *Orchestrator) GetHost(hostID string) (domain.Host, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.findHost(hostID)
}

func (s *Orchestrator) ListProducts() ([]domain.Product, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.store != nil {
		return s.store.ListProducts()
	}
	products := make([]domain.Product, 0, len(s.registry.Products))
	for _, product := range s.registry.Products {
		products = append(products, product)
	}
	return products, nil
}

func (s *Orchestrator) ListProviderAccounts(hostID string) ([]domain.HostProviderAccount, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.store != nil {
		return s.store.GetProviderAccounts(hostID)
	}
	return append([]domain.HostProviderAccount{}, s.registry.HostProviderAccts[hostID]...), nil
}

func (s *Orchestrator) ListOrders() ([]domain.PaymentOrder, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.store != nil {
		return s.store.ListOrders()
	}
	orders := make([]domain.PaymentOrder, 0, len(s.orders))
	for _, order := range s.orders {
		orders = append(orders, order)
	}
	return orders, nil
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

func (s *Orchestrator) findHost(hostID string) (domain.Host, error) {
	if hostID == "" {
		return domain.Host{}, errors.New("host id is required")
	}
	if s.store != nil {
		return s.store.GetHost(hostID)
	}
	host, ok := s.registry.Hosts[hostID]
	if !ok {
		return domain.Host{}, errors.New("host not found")
	}
	return host, nil
}

func (s *Orchestrator) findProduct(productID string) (domain.Product, error) {
	if productID == "" {
		return domain.Product{}, errors.New("product id is required")
	}
	if s.store != nil {
		return s.store.GetProduct(productID)
	}
	product, ok := s.registry.Products[productID]
	if !ok {
		return domain.Product{}, errors.New("product not found")
	}
	return product, nil
}

func (s *Orchestrator) findHostPolicy(hostID string) (domain.FeePolicy, error) {
	if s.store != nil {
		return s.store.GetHostPolicy(hostID)
	}
	policy, ok := s.registry.HostPolicies[hostID]
	if !ok {
		return domain.FeePolicy{}, errors.New("policy not found")
	}
	return policy, nil
}

func (s *Orchestrator) selectProvider(hostID, env string) (string, error) {
	if s.store != nil {
		accounts, err := s.store.GetProviderAccounts(hostID)
		if err != nil {
			return "", err
		}
		if len(accounts) == 0 {
			return "", errors.New("provider account not configured")
		}
		for _, acct := range accounts {
			if acct.Env == env {
				return acct.Provider, nil
			}
		}
		for _, acct := range accounts {
			if acct.Env == "sandbox" {
				return acct.Provider, nil
			}
		}
		return accounts[0].Provider, nil
	}
	accts := s.registry.HostProviderAccts[hostID]
	for _, acct := range accts {
		if acct.Env == env {
			return acct.Provider, nil
		}
	}
	for _, acct := range accts {
		if acct.Env == "sandbox" {
			return acct.Provider, nil
		}
	}
	if len(accts) == 0 {
		return "", errors.New("provider account not configured")
	}
	return accts[0].Provider, nil
}

func (s *Orchestrator) saveOrder(order domain.PaymentOrder, ledger domain.PaymentOrderLedger) error {
	if s.store != nil {
		return s.store.SaveOrder(order, ledger)
	}
	s.orders[order.Reference] = order
	s.ledgers[order.Reference] = ledger
	return nil
}

func (s *Orchestrator) getOrderLocked(reference string) (domain.PaymentOrder, error) {
	if s.store != nil {
		order, err := s.store.GetOrder(reference)
		if err != nil {
			s.metrics.Inc("payments.get.not_found")
			return domain.PaymentOrder{}, err
		}
		return order, nil
	}
	order, ok := s.orders[reference]
	if !ok {
		s.metrics.Inc("payments.get.not_found")
		return domain.PaymentOrder{}, errors.New("payment not found")
	}
	return order, nil
}

func (s *Orchestrator) getLedgerLocked(reference string) (domain.PaymentOrderLedger, error) {
	if s.store != nil {
		ledger, err := s.store.GetLedger(reference)
		if err != nil {
			s.metrics.Inc("payments.ledger.not_found")
			return domain.PaymentOrderLedger{}, err
		}
		return ledger, nil
	}
	ledger, ok := s.ledgers[reference]
	if !ok {
		return domain.PaymentOrderLedger{}, errors.New("ledger not found")
	}
	return ledger, nil
}

func (s *Orchestrator) filterExistingAccounts(accounts []domain.HostProviderAccount, next domain.HostProviderAccount) []domain.HostProviderAccount {
	filtered := accounts[:0]
	for _, account := range accounts {
		if account.Provider == next.Provider && account.Env == next.Env {
			continue
		}
		filtered = append(filtered, account)
	}
	return append(filtered, next)
}

func (s *Orchestrator) defaultPolicy() domain.FeePolicy {
	return domain.FeePolicy{
		Type:     domain.FeeTypeFree,
		Value:    0,
		Currency: "IDR",
		Rounding: domain.RoundingRuleNearest,
	}
}

func newReference() string {
	buf := make([]byte, 8)
	_, _ = rand.Read(buf)
	return fmt.Sprintf("ord-%d-%s", time.Now().UnixNano(), hex.EncodeToString(buf))
}

// DeleteHost removes host configuration and related dependencies.
func (s *Orchestrator) DeleteHost(hostID string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.store != nil {
		type hostDeleter interface {
			DeleteHost(id string) error
		}
		if hd, ok := s.store.(hostDeleter); ok {
			return hd.DeleteHost(hostID)
		}
		return errors.New("underlying store does not support host deletion")
	}
	delete(s.registry.Hosts, hostID)
	delete(s.registry.HostPolicies, hostID)
	delete(s.registry.HostProviderAccts, hostID)
	for k, v := range s.registry.Products {
		if v.HostID == hostID {
			delete(s.registry.Products, k)
		}
	}
	return nil
}

// DeleteProduct removes product config.
func (s *Orchestrator) DeleteProduct(productID string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.store != nil {
		type productDeleter interface {
			DeleteProduct(id string) error
		}
		if pd, ok := s.store.(productDeleter); ok {
			return pd.DeleteProduct(productID)
		}
		return errors.New("underlying store does not support product deletion")
	}
	delete(s.registry.Products, productID)
	return nil
}

// DeleteProviderAccount removes host provider account configs.
func (s *Orchestrator) DeleteProviderAccount(hostID, provider, env string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.store != nil {
		type providerDeleter interface {
			DeleteProviderAccount(hostID, provider, env string) error
		}
		if pd, ok := s.store.(providerDeleter); ok {
			return pd.DeleteProviderAccount(hostID, provider, env)
		}
		return errors.New("underlying store does not support provider account deletion")
	}
	accts := s.registry.HostProviderAccts[hostID]
	filtered := accts[:0]
	for _, acct := range accts {
		if acct.Provider == provider && acct.Env == env {
			continue
		}
		filtered = append(filtered, acct)
	}
	s.registry.HostProviderAccts[hostID] = filtered
	return nil
}

