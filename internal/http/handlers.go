package httphandlers

import (
	"bytes"
	"encoding/json"
	"fmt"
	"html/template"
	"io"
	"net/http"
	"sort"
	"strings"
	"time"

	"github.com/pendig/host-rutebayar/internal/domain"
	"github.com/pendig/host-rutebayar/internal/orchestration"
	"github.com/pendig/host-rutebayar/internal/security"
)

// CreatePaymentRequest is payload for POST /payments.
type CreatePaymentRequest struct {
	HostID    string `json:"host_id"`
	ProductID string `json:"product_id"`
	BuyerRef  string `json:"buyer_ref"`
	Env       string `json:"env"`
}

type createPaymentResponse struct {
	Reference   string `json:"reference"`
	Status      string `json:"status"`
	CheckoutURL string `json:"checkout_url"`
}

// PaymentStatusResponse is returned by GET /payments/{reference}.
type PaymentStatusResponse struct {
	Reference   string `json:"reference"`
	Status      string `json:"status"`
	HostID      string `json:"host_id"`
	ProductID   string `json:"product_id"`
	CheckoutURL string `json:"checkout_url"`
	HostFee     int64  `json:"host_fee_amount"`
	Gross       int64  `json:"gross_amount"`
	Net         int64  `json:"net_amount"`
}

type webhookPayload struct {
	Reference      string `json:"reference"`
	Status         string `json:"status"`
	IdempotencyKey string `json:"idempotency_key"`
}

type registerHostRequest struct {
	ID                string   `json:"id"`
	Name              string   `json:"name"`
	CallbackURLs      []string `json:"callback_urls"`
	CallbackAllowlist []string `json:"callback_allowlist"`
	NotificationKey   string   `json:"notification_key"`
	HostSecret        string   `json:"host_secret"`
	WebhookSecret     string   `json:"webhook_secret"`
}

type feePolicyInput struct {
	Type          string  `json:"type"`
	Value         float64 `json:"value"`
	Currency      string  `json:"currency"`
	Rounding      string  `json:"rounding"`
	MinFee        *int64  `json:"min_fee"`
	MaxFee        *int64  `json:"max_fee"`
	PolicyVersion string  `json:"policy_version"`
}

type registerProductRequest struct {
	ID                string            `json:"id"`
	HostID            string            `json:"host_id"`
	Name              string            `json:"name"`
	SKU               string            `json:"sku"`
	Price             int64             `json:"price"`
	IsActive          bool              `json:"is_active"`
	Meta              map[string]string `json:"meta"`
	FeePolicyOverride *feePolicyInput   `json:"fee_policy_override"`
}

type registerProviderRequest struct {
	HostID          string            `json:"host_id"`
	Provider        string            `json:"provider"`
	Env             string            `json:"env"`
	CredentialsHash string            `json:"credentials_hash"`
	PublicConfig    map[string]string `json:"public_config"`
}

type registerHostPolicyRequest struct {
	HostID         string         `json:"host_id"`
	FeePolicyInput feePolicyInput `json:"fee_policy"`
}

type registerResponse struct {
	ID      string `json:"id"`
	Message string `json:"message"`
}

func SetupMux(orchestrator *orchestration.Orchestrator) *http.ServeMux {
	mux := http.NewServeMux()
	mux.HandleFunc("/health", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]string{"status": "ok"})
	})
	mux.HandleFunc("/ui", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		handleUI(w, r, orchestrator)
	})
	mux.HandleFunc("/register/host", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		handleRegisterHost(w, r, orchestrator)
	})
	mux.HandleFunc("/register/product", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		handleRegisterProduct(w, r, orchestrator)
	})
	mux.HandleFunc("/register/provider-account", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		handleRegisterProviderAccount(w, r, orchestrator)
	})
	mux.HandleFunc("/register/host-policy", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		handleRegisterHostPolicy(w, r, orchestrator)
	})
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
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/" {
			http.Redirect(w, r, "/ui", http.StatusMovedPermanently)
			return
		}
		http.NotFound(w, r)
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
	if err := authorizeHostSecret(r, orchestrator, req.HostID); err != nil {
		http.Error(w, err.Error(), http.StatusUnauthorized)
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
	response := createPaymentResponse{Reference: out.Reference, Status: string(out.Order.Status), CheckoutURL: out.Order.ProviderCheckoutURL}
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusAccepted)
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
	response := PaymentStatusResponse{
		Reference:   order.Reference,
		Status:      string(order.Status),
		HostID:      order.HostID,
		ProductID:   order.ProductID,
		CheckoutURL: order.ProviderCheckoutURL,
		HostFee:     order.HostFeeAmount,
		Gross:       order.GrossAmount,
		Net:         order.NetAmount,
	}
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(response)
}

func handleWebhook(w http.ResponseWriter, r *http.Request, orchestrator *orchestration.Orchestrator) {
	provider := strings.TrimPrefix(r.URL.Path, "/webhooks/")
	if provider == "" {
		http.Error(w, "provider is required", http.StatusBadRequest)
		return
	}
	body, err := io.ReadAll(r.Body)
	if err != nil {
		http.Error(w, "invalid body", http.StatusBadRequest)
		return
	}
	var payload webhookPayload
	if err := json.NewDecoder(bytes.NewReader(body)).Decode(&payload); err != nil {
		http.Error(w, "invalid body", http.StatusBadRequest)
		return
	}
	if payload.Reference == "" || payload.Status == "" || payload.IdempotencyKey == "" {
		http.Error(w, "reference, status, and idempotency_key are required", http.StatusBadRequest)
		return
	}
	if err := authorizeWebhookSignature(r, orchestrator, payload.Reference, body); err != nil {
		http.Error(w, err.Error(), http.StatusUnauthorized)
		return
	}
	status, err := orchestrator.ReconcileWebhook(payload.Reference, provider, payload.Status, payload.IdempotencyKey)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusAccepted)
	_ = json.NewEncoder(w).Encode(map[string]string{"status": string(status)})
}

func handleRegisterHost(w http.ResponseWriter, r *http.Request, orchestrator *orchestration.Orchestrator) {
	var req registerHostRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, "invalid body", http.StatusBadRequest)
		return
	}
	if req.ID == "" || req.Name == "" || req.NotificationKey == "" || req.HostSecret == "" || req.WebhookSecret == "" {
		http.Error(w, "id, name, notification_key, host_secret, and webhook_secret are required", http.StatusBadRequest)
		return
	}
	host := domain.Host{
		ID:                req.ID,
		Name:              req.Name,
		CallbackURLs:      req.CallbackURLs,
		CallbackAllowlist: req.CallbackAllowlist,
		NotificationKey:   req.NotificationKey,
		HostSecret:        req.HostSecret,
		WebhookSecret:     req.WebhookSecret,
	}
	if err := orchestrator.RegisterHost(host); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusCreated)
	_ = json.NewEncoder(w).Encode(registerResponse{ID: req.ID, Message: "host registered"})
}

func handleRegisterProduct(w http.ResponseWriter, r *http.Request, orchestrator *orchestration.Orchestrator) {
	var req registerProductRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, "invalid body", http.StatusBadRequest)
		return
	}
	if req.ID == "" || req.HostID == "" || req.Name == "" || req.Price < 0 {
		http.Error(w, "id, host_id, name, and price are required", http.StatusBadRequest)
		return
	}
	if err := authorizeHostSecret(r, orchestrator, req.HostID); err != nil {
		http.Error(w, err.Error(), http.StatusUnauthorized)
		return
	}
	product := domain.Product{
		ID:       req.ID,
		HostID:   req.HostID,
		Name:     req.Name,
		SKU:      req.SKU,
		Price:    req.Price,
		IsActive: req.IsActive,
		Meta:     req.Meta,
	}
	if product.Meta == nil {
		product.Meta = map[string]string{}
	}
	if req.FeePolicyOverride != nil {
		policy := domain.FeePolicy{
			Type:          domain.FeeType(req.FeePolicyOverride.Type),
			Value:         req.FeePolicyOverride.Value,
			Currency:      req.FeePolicyOverride.Currency,
			Rounding:      domain.RoundingRule(req.FeePolicyOverride.Rounding),
			MinFee:        req.FeePolicyOverride.MinFee,
			MaxFee:        req.FeePolicyOverride.MaxFee,
			PolicyVersion: req.FeePolicyOverride.PolicyVersion,
		}
		product.FeePolicyOverride = &policy
	}
	if err := orchestrator.RegisterProduct(product); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusCreated)
	_ = json.NewEncoder(w).Encode(registerResponse{ID: req.ID, Message: "product registered"})
}

func handleRegisterProviderAccount(w http.ResponseWriter, r *http.Request, orchestrator *orchestration.Orchestrator) {
	var req registerProviderRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, "invalid body", http.StatusBadRequest)
		return
	}
	if req.HostID == "" || req.Provider == "" || req.Env == "" || req.CredentialsHash == "" {
		http.Error(w, "host_id, provider, env, and credentials_hash are required", http.StatusBadRequest)
		return
	}
	if err := authorizeHostSecret(r, orchestrator, req.HostID); err != nil {
		http.Error(w, err.Error(), http.StatusUnauthorized)
		return
	}
	if req.PublicConfig == nil {
		req.PublicConfig = map[string]string{}
	}
	account := domain.HostProviderAccount{
		HostID:          req.HostID,
		Provider:        req.Provider,
		Env:             req.Env,
		CredentialsHash: req.CredentialsHash,
		PublicConfig:    req.PublicConfig,
	}
	if err := orchestrator.RegisterProviderAccount(account); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusCreated)
	_ = json.NewEncoder(w).Encode(registerResponse{ID: req.HostID, Message: "provider account registered"})
}

func handleRegisterHostPolicy(w http.ResponseWriter, r *http.Request, orchestrator *orchestration.Orchestrator) {
	var req registerHostPolicyRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, "invalid body", http.StatusBadRequest)
		return
	}
	if req.HostID == "" {
		http.Error(w, "host_id is required", http.StatusBadRequest)
		return
	}
	if err := authorizeHostSecret(r, orchestrator, req.HostID); err != nil {
		http.Error(w, err.Error(), http.StatusUnauthorized)
		return
	}
	policy := domain.FeePolicy{
		Type:          domain.FeeType(req.FeePolicyInput.Type),
		Value:         req.FeePolicyInput.Value,
		Currency:      req.FeePolicyInput.Currency,
		Rounding:      domain.RoundingRule(req.FeePolicyInput.Rounding),
		MinFee:        req.FeePolicyInput.MinFee,
		MaxFee:        req.FeePolicyInput.MaxFee,
		PolicyVersion: req.FeePolicyInput.PolicyVersion,
	}
	if err := orchestrator.RegisterHostPolicy(req.HostID, policy); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusCreated)
	_ = json.NewEncoder(w).Encode(registerResponse{ID: req.HostID, Message: "host policy registered"})
}

func authorizeHostSecret(r *http.Request, orchestrator *orchestration.Orchestrator, hostID string) error {
	host, err := orchestrator.GetHost(hostID)
	if err != nil {
		return err
	}
	if r.Header.Get("X-Host-Secret") != host.HostSecret {
		return fmt.Errorf("invalid host secret")
	}
	return nil
}

func authorizeWebhookSignature(r *http.Request, orchestrator *orchestration.Orchestrator, reference string, body []byte) error {
	order, err := orchestrator.GetPayment(reference)
	if err != nil {
		return err
	}
	host, err := orchestrator.GetHost(order.HostID)
	if err != nil {
		return err
	}
	signature := r.Header.Get("X-Webhook-Signature")
	if signature == "" {
		return fmt.Errorf("missing webhook signature")
	}
	ring := security.SignatureRing{Current: host.WebhookSecret}
	if err := ring.VerifySignature(body, signature, 5*time.Minute, time.Now().UTC()); err != nil {
		return fmt.Errorf("invalid webhook signature")
	}
	return nil
}

func handleUI(w http.ResponseWriter, r *http.Request, orchestrator *orchestration.Orchestrator) {
	hosts, err := orchestrator.ListHosts()
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	products, err := orchestrator.ListProducts()
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	orders, err := orchestrator.ListOrders()
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	sort.Slice(hosts, func(i, j int) bool { return hosts[i].ID < hosts[j].ID })
	sort.Slice(products, func(i, j int) bool { return products[i].ID < products[j].ID })
	sort.Slice(orders, func(i, j int) bool { return orders[i].Reference > orders[j].Reference })

	tmpl, err := template.New("ui").Parse(dashboardHTML)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	_ = tmpl.Execute(w, map[string]interface{}{
		"Hosts":    hosts,
		"Products": products,
		"Orders":   orders,
	})
}

const dashboardHTML = `<!doctype html>
<html>
<head>
	<meta charset="utf-8"/>
	<title>host-rutebayar dashboard</title>
	<style>
		body { font-family: Inter, Arial, sans-serif; margin: 0; padding: 20px; background: linear-gradient(120deg, #f3f7ff, #eef8f7); color: #15314b; }
		table { border-collapse: collapse; width: 100%; margin-bottom: 24px; }
		td, th { border: 1px solid #c4d0dc; padding: 8px; text-align: left; }
		th { background: #1f3c5d; color: #fff; position: sticky; top: 0; }
		h1 { margin-top: 0; }
		pre { background: #f8fcff; padding: 8px; border: 1px solid #d6e4f0; }
		.section { background: #fff; border-radius: 10px; padding: 16px; margin-bottom: 24px; box-shadow: 0 8px 24px rgba(2, 53, 110, 0.08); }
	</style>
</head>
<body>
	<h1>host-rutebayar self hosted</h1>
	<p>Monitoring dan registrasi sederhana untuk host, produk, dan pembayaran.</p>
	<div class="section">
		<h2>Hosts</h2>
		<table>
			<tr><th>ID</th><th>Nama</th><th>Callback URL</th><th>Allowlist</th></tr>
			{{range .Hosts}}
			<tr>
				<td>{{.ID}}</td>
				<td>{{.Name}}</td>
				<td><pre>{{range .CallbackURLs}}{{.}} {{end}}</pre></td>
				<td><pre>{{range .CallbackAllowlist}}{{.}} {{end}}</pre></td>
			</tr>
			{{else}}
			<tr><td colspan="4">Belum ada host terdaftar.</td></tr>
			{{end}}
		</table>
	</div>
	<div class="section">
		<h2>Products</h2>
		<table>
			<tr><th>ID</th><th>Host ID</th><th>Nama</th><th>SKU</th><th>Harga</th><th>Active</th><th>Policy Override</th></tr>
			{{range .Products}}
			<tr>
				<td>{{.ID}}</td>
				<td>{{.HostID}}</td>
				<td>{{.Name}}</td>
				<td>{{.SKU}}</td>
				<td>{{.Price}}</td>
				<td>{{.IsActive}}</td>
				<td>{{if .FeePolicyOverride}}yes{{else}}no{{end}}</td>
			</tr>
			{{else}}
			<tr><td colspan="7">Belum ada produk terdaftar.</td></tr>
			{{end}}
		</table>
	</div>
	<div class="section">
		<h2>Orders</h2>
		<table>
			<tr><th>Reference</th><th>Status</th><th>Host</th><th>Product</th><th>Provider</th><th>Gross</th><th>Host Fee</th><th>Net</th><th>Checkout URL</th></tr>
			{{range .Orders}}
			<tr>
				<td>{{.Reference}}</td>
				<td>{{.Status}}</td>
				<td>{{.HostID}}</td>
				<td>{{.ProductID}}</td>
				<td>{{.Provider}}</td>
				<td>{{.GrossAmount}}</td>
				<td>{{.HostFeeAmount}}</td>
				<td>{{.NetAmount}}</td>
				<td><a href="{{.ProviderCheckoutURL}}">{{if .ProviderCheckoutURL}}open{{else}}-{{end}}</a></td>
			</tr>
			{{else}}
			<tr><td colspan="9">Belum ada order.</td></tr>
			{{end}}
		</table>
	</div>
</body>
</html>`
