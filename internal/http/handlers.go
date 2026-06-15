package httphandlers

import (
	"bytes"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"html/template"
	"io"
	"net/http"
	"net/url"
	"sort"
	"strings"
	"sync"
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

type testPaymentRequest struct {
	HostID    string `json:"host_id"`
	ProductID string `json:"product_id"`
	BuyerRef  string `json:"buyer_ref"`
	Env       string `json:"env"`
}

type seedDataResponse struct {
	HostID    string `json:"host_id"`
	ProductID string `json:"product_id"`
	Reference string `json:"reference,omitempty"`
	Message   string `json:"message"`
}

type replayCallbackRequest struct {
	Reference      string `json:"reference"`
	Provider       string `json:"provider"`
	Status         string `json:"status"`
	IdempotencyKey string `json:"idempotency_key"`
}

type uiCallbackDelivery struct {
	At             string `json:"at"`
	Reference      string `json:"reference"`
	Provider       string `json:"provider"`
	Status         string `json:"status"`
	Result         string `json:"result"`
	IdempotencyKey string `json:"idempotency_key"`
	Attempts       int    `json:"attempts"`
	Error          string `json:"error"`
}

var (
	callbackLogMu sync.Mutex
	callbackLogs  = []uiCallbackDelivery{}
	uiSessionMu   sync.Mutex
	uiSessions    = map[string]time.Time{}
)

const (
	uiSessionCookieName = "host-rutebayar-admin-session"
	uiSessionTTL        = 12 * time.Hour
	maxCallbackLogs     = 100
)

func hasAdminUISession(r *http.Request) bool {
	cookie, err := r.Cookie(uiSessionCookieName)
	if err != nil {
		return false
	}
	uiSessionMu.Lock()
	defer uiSessionMu.Unlock()
	expiresAt, ok := uiSessions[cookie.Value]
	if !ok {
		return false
	}
	if time.Now().After(expiresAt) {
		delete(uiSessions, cookie.Value)
		return false
	}
	uiSessions[cookie.Value] = time.Now().Add(uiSessionTTL)
	return true
}

func sanitizeUISessionNext(next string) string {
	next = strings.TrimSpace(next)
	if next == "" || !strings.HasPrefix(next, "/") {
		return "/ui"
	}
	return next
}

func generateUISessionToken() (string, error) {
	buf := make([]byte, 32)
	if _, err := rand.Read(buf); err != nil {
		return "", err
	}
	return base64.RawURLEncoding.EncodeToString(buf), nil
}

func generateRandomToken(size int) (string, error) {
	if size <= 0 {
		size = 16
	}
	buf := make([]byte, size)
	if _, err := rand.Read(buf); err != nil {
		return "", err
	}
	return base64.RawURLEncoding.EncodeToString(buf), nil
}

func setAdminUISession(w http.ResponseWriter, token string) {
	expiresAt := time.Now().Add(uiSessionTTL)
	uiSessionMu.Lock()
	uiSessions[token] = expiresAt
	uiSessionMu.Unlock()
	http.SetCookie(w, &http.Cookie{
		Name:     uiSessionCookieName,
		Value:    token,
		Path:     "/",
		HttpOnly: true,
		SameSite: http.SameSiteLaxMode,
		Expires:  expiresAt,
		MaxAge:   int(uiSessionTTL.Seconds()),
	})
}

func clearAdminUISession(w http.ResponseWriter, r *http.Request) {
	cookie, err := r.Cookie(uiSessionCookieName)
	if err == nil {
		uiSessionMu.Lock()
		delete(uiSessions, cookie.Value)
		uiSessionMu.Unlock()
	}
	http.SetCookie(w, &http.Cookie{
		Name:     uiSessionCookieName,
		Path:     "/",
		HttpOnly: true,
		SameSite: http.SameSiteLaxMode,
		Expires:  time.Unix(0, 0),
		MaxAge:   -1,
	})
}

func recordCallbackLog(entry uiCallbackDelivery) {
	callbackLogMu.Lock()
	defer callbackLogMu.Unlock()
	callbackLogs = append(callbackLogs, entry)
	if len(callbackLogs) > maxCallbackLogs {
		callbackLogs = callbackLogs[len(callbackLogs)-maxCallbackLogs:]
	}
}

func listCallbackLogs() []uiCallbackDelivery {
	callbackLogMu.Lock()
	defer callbackLogMu.Unlock()
	out := make([]uiCallbackDelivery, 0, len(callbackLogs))
	out = append(out, callbackLogs...)
	for i, j := 0, len(out)-1; i < j; i, j = i+1, j-1 {
		out[i], out[j] = out[j], out[i]
	}
	if len(out) > maxCallbackLogs {
		out = out[:maxCallbackLogs]
	}
	return out
}

func SetupMux(orchestrator *orchestration.Orchestrator, adminPassword ...string) *http.ServeMux {
	password := "admin123"
	if len(adminPassword) > 0 {
		password = strings.TrimSpace(adminPassword[0])
		if password == "" {
			password = "admin123"
		}
	}
	requireAdminSession := func(handler http.HandlerFunc) http.HandlerFunc {
		return func(w http.ResponseWriter, r *http.Request) {
			if hasAdminUISession(r) {
				handler(w, r)
				return
			}
			if r.Method == http.MethodGet {
				next := r.URL.Path
				if r.URL.RawQuery != "" {
					next += "?" + r.URL.RawQuery
				}
				http.Redirect(w, r, "/ui/login?next="+url.QueryEscape(next), http.StatusFound)
				return
			}
			http.Error(w, "unauthorized", http.StatusUnauthorized)
		}
	}

	mux := http.NewServeMux()
	mux.HandleFunc("/health", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]string{"status": "ok"})
	})
	mux.HandleFunc("/ui/login", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet && r.Method != http.MethodPost {
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		handleUILogin(w, r, password)
	})
	mux.HandleFunc("/ui/logout", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet && r.Method != http.MethodPost {
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		handleUILogout(w, r)
	})
	mux.HandleFunc("/ui/host/", requireAdminSession(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		handleUIHost(w, r, orchestrator)
	}))
	mux.HandleFunc("/ui/product/", requireAdminSession(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		handleUIProduct(w, r, orchestrator)
	}))
	mux.HandleFunc("/ui/order/", requireAdminSession(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		handleUIOrder(w, r, orchestrator)
	}))
	mux.HandleFunc("/ui/callbacks", requireAdminSession(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		handleUICallbacks(w, r)
	}))
	mux.HandleFunc("/ui/callbacks/replay", requireAdminSession(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		handleReplayCallback(w, r, orchestrator)
	}))
	mux.HandleFunc("/ui", requireAdminSession(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		handleUI(w, r, orchestrator)
	}))
	mux.HandleFunc("/admin/demo-seed", requireAdminSession(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		handleDemoSeed(w, r, orchestrator)
	}))
	mux.HandleFunc("/admin/test-payment", requireAdminSession(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		handleCreateTestPayment(w, r, orchestrator)
	}))
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

func handleCreateTestPayment(w http.ResponseWriter, r *http.Request, orchestrator *orchestration.Orchestrator) {
	var req testPaymentRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, "invalid body", http.StatusBadRequest)
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
	response := createPaymentResponse{
		Reference:   out.Reference,
		Status:      string(out.Order.Status),
		CheckoutURL: out.Order.ProviderCheckoutURL,
	}
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusAccepted)
	_ = json.NewEncoder(w).Encode(response)
}

func handleDemoSeed(w http.ResponseWriter, r *http.Request, orchestrator *orchestration.Orchestrator) {
	hostSecret, err := generateRandomToken(20)
	if err != nil {
		http.Error(w, "unable to generate host secret", http.StatusInternalServerError)
		return
	}
	webhookSecret, err := generateRandomToken(20)
	if err != nil {
		http.Error(w, "unable to generate webhook secret", http.StatusInternalServerError)
		return
	}
	notificationKey, err := generateRandomToken(20)
	if err != nil {
		http.Error(w, "unable to generate notification key", http.StatusInternalServerError)
		return
	}
	host := domain.Host{
		ID:                "host-demo",
		Name:              "Demo Host",
		CallbackURLs:      []string{"https://example.com/callback"},
		CallbackAllowlist: []string{"https://example.com"},
		NotificationKey:   notificationKey,
		HostSecret:        hostSecret,
		WebhookSecret:     webhookSecret,
	}
	product := domain.Product{
		ID:       "prod-demo-001",
		HostID:   host.ID,
		Name:     "Paket Demo",
		SKU:      "PKT-001",
		Price:    120000,
		IsActive: true,
	}
	account := domain.HostProviderAccount{
		HostID:          host.ID,
		Provider:        "midtrans",
		Env:             "sandbox",
		CredentialsHash: "sha256:demo-credentials-hash",
		PublicConfig: map[string]string{
			"merchant_id": "m-demo",
		},
	}
	if err := orchestrator.RegisterHost(host); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	if err := orchestrator.RegisterProduct(product); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	if err := orchestrator.RegisterProviderAccount(account); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	if err := orchestrator.RegisterHostPolicy(host.ID, domain.FeePolicy{
		Type:          domain.FeeTypePercent,
		Value:         2,
		Currency:      "IDR",
		Rounding:      domain.RoundingRuleNearest,
		PolicyVersion: "v1",
	}); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	payment, err := orchestrator.CreatePayment(orchestration.CreateInput{
		HostID:    host.ID,
		ProductID: product.ID,
		Env:       "sandbox",
		BuyerRef:  "seed-order",
	})
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	res := seedDataResponse{
		HostID:    host.ID,
		ProductID: product.ID,
		Reference: payment.Reference,
		Message:   "seed demo siap. Host, product, provider account, policy, dan contoh order sudah dibuat.",
	}
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(res)
}

func handleUICallbacks(w http.ResponseWriter, r *http.Request) {
	logs := listCallbackLogs()
	tmpl, err := template.New("uiCallbacks").Parse(uiCallbacksHTML)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	_ = tmpl.Execute(w, map[string]interface{}{
		"Deliveries": logs,
	})
}

func handleUILogin(w http.ResponseWriter, r *http.Request, adminPassword string) {
	if hasAdminUISession(r) {
		next := sanitizeUISessionNext(r.URL.Query().Get("next"))
		http.Redirect(w, r, next, http.StatusFound)
		return
	}
	tmpl, err := template.New("uiLogin").Parse(uiLoginHTML)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	if r.Method == http.MethodGet {
		message := strings.TrimSpace(r.URL.Query().Get("error")) == "1"
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		_ = tmpl.Execute(w, map[string]interface{}{
			"Next":    sanitizeUISessionNext(r.URL.Query().Get("next")),
			"Message": message,
		})
		return
	}
	if err := r.ParseForm(); err != nil {
		http.Error(w, "invalid form", http.StatusBadRequest)
		return
	}
	next := sanitizeUISessionNext(r.FormValue("next"))
	password := strings.TrimSpace(r.FormValue("password"))
	if password != adminPassword {
		http.Redirect(w, r, "/ui/login?next="+url.QueryEscape(next)+"&error=1", http.StatusFound)
		return
	}
	token, err := generateUISessionToken()
	if err != nil {
		http.Error(w, "unable to create session", http.StatusInternalServerError)
		return
	}
	setAdminUISession(w, token)
	http.Redirect(w, r, next, http.StatusFound)
}

func handleUILogout(w http.ResponseWriter, r *http.Request) {
	clearAdminUISession(w, r)
	http.Redirect(w, r, "/ui/login", http.StatusFound)
}

func handleReplayCallback(w http.ResponseWriter, r *http.Request, orchestrator *orchestration.Orchestrator) {
	if !hasAdminUISession(r) {
		http.Error(w, "unauthorized", http.StatusUnauthorized)
		return
	}
	body, err := io.ReadAll(r.Body)
	if err != nil {
		http.Error(w, "invalid body", http.StatusBadRequest)
		return
	}
	var req replayCallbackRequest
	if err := json.Unmarshal(body, &req); err != nil {
		http.Error(w, "invalid body", http.StatusBadRequest)
		return
	}
	if req.Reference == "" || req.Provider == "" || req.Status == "" || req.IdempotencyKey == "" {
		http.Error(w, "reference, provider, status, and idempotency_key are required", http.StatusBadRequest)
		return
	}
	signature := strings.TrimSpace(r.Header.Get("X-Webhook-Signature"))
	if signature != "" {
		if err := authorizeWebhookSignature(r, orchestrator, req.Reference, body); err != nil {
			recordCallbackLog(uiCallbackDelivery{
				At:             time.Now().UTC().Format(time.RFC3339),
				Reference:      req.Reference,
				Provider:       req.Provider,
				Status:         req.Status,
				Result:         "replay-failed",
				IdempotencyKey: req.IdempotencyKey,
				Error:          err.Error(),
			})
			http.Error(w, err.Error(), http.StatusUnauthorized)
			return
		}
	}
	status, attempts, err := orchestrator.ReconcileWebhookWithRetryWithAttempts(req.Reference, req.Provider, req.Status, req.IdempotencyKey)
	if err != nil {
		recordCallbackLog(uiCallbackDelivery{
			At:             time.Now().UTC().Format(time.RFC3339),
			Reference:      req.Reference,
			Provider:       req.Provider,
			Status:         req.Status,
			Result:         "replay-failed",
			IdempotencyKey: req.IdempotencyKey,
			Attempts:       attempts,
			Error:          err.Error(),
		})
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	recordCallbackLog(uiCallbackDelivery{
		At:             time.Now().UTC().Format(time.RFC3339),
		Reference:      req.Reference,
		Provider:       req.Provider,
		Status:         string(status),
		Result:         "replay-success",
		IdempotencyKey: req.IdempotencyKey,
		Attempts:       attempts,
	})
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(map[string]string{
		"reference": req.Reference,
		"status":    string(status),
		"result":    "replayed",
	})
}

func handleUIHost(w http.ResponseWriter, r *http.Request, orchestrator *orchestration.Orchestrator) {
	hostID := strings.TrimPrefix(r.URL.Path, "/ui/host/")
	if hostID == "" {
		http.Error(w, "host id is required", http.StatusBadRequest)
		return
	}
	host, err := orchestrator.GetHost(hostID)
	if err != nil {
		if isNotFoundErr(err) {
			http.Error(w, err.Error(), http.StatusNotFound)
		} else {
			http.Error(w, err.Error(), http.StatusInternalServerError)
		}
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
	accounts, err := orchestrator.ListProviderAccounts(hostID)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	var hostProducts []domain.Product
	var hostOrders []domain.PaymentOrder
	for _, p := range products {
		if p.HostID == hostID {
			hostProducts = append(hostProducts, p)
		}
	}
	for _, o := range orders {
		if o.HostID == hostID {
			hostOrders = append(hostOrders, o)
		}
	}
	sort.Slice(hostProducts, func(i, j int) bool { return hostProducts[i].ID < hostProducts[j].ID })
	sort.Slice(hostOrders, func(i, j int) bool { return hostOrders[i].Reference > hostOrders[j].Reference })

	tmpl, err := template.New("uiHost").Parse(uiHostHTML)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	_ = tmpl.Execute(w, map[string]interface{}{
		"Host":     host,
		"Products": hostProducts,
		"Orders":   hostOrders,
		"Accounts": accounts,
	})
}

func handleUIProduct(w http.ResponseWriter, r *http.Request, orchestrator *orchestration.Orchestrator) {
	productID := strings.TrimPrefix(r.URL.Path, "/ui/product/")
	if productID == "" {
		http.Error(w, "product id is required", http.StatusBadRequest)
		return
	}
	products, err := orchestrator.ListProducts()
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	var product domain.Product
	var found bool
	for _, candidate := range products {
		if candidate.ID == productID {
			product = candidate
			found = true
			break
		}
	}
	if !found {
		http.NotFound(w, r)
		return
	}
	tmpl, err := template.New("uiProduct").Parse(uiProductHTML)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	_ = tmpl.Execute(w, map[string]interface{}{
		"Product": product,
	})
}

func handleUIOrder(w http.ResponseWriter, r *http.Request, orchestrator *orchestration.Orchestrator) {
	reference := strings.TrimPrefix(r.URL.Path, "/ui/order/")
	if reference == "" {
		http.Error(w, "reference is required", http.StatusBadRequest)
		return
	}
	order, err := orchestrator.GetPayment(reference)
	if err != nil {
		if isNotFoundErr(err) {
			http.Error(w, err.Error(), http.StatusNotFound)
		} else {
			http.Error(w, err.Error(), http.StatusInternalServerError)
		}
		return
	}
	ledger, err := orchestrator.GetLedger(reference)
	hasLedger := true
	if err != nil {
		if isNotFoundErr(err) {
			ledger = domain.PaymentOrderLedger{}
			hasLedger = false
		} else {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
	}
	tmpl, err := template.New("uiOrder").Parse(uiOrderHTML)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	_ = tmpl.Execute(w, map[string]interface{}{
		"Order":     order,
		"Ledger":    ledger,
		"HasLedger": hasLedger,
	})
}

func handleWebhook(w http.ResponseWriter, r *http.Request, orchestrator *orchestration.Orchestrator) {
	provider := strings.TrimPrefix(r.URL.Path, "/webhooks/")
	if provider == "" {
		http.Error(w, "provider is required", http.StatusBadRequest)
		return
	}
	body, err := io.ReadAll(r.Body)
	if err != nil {
		recordCallbackLog(uiCallbackDelivery{
			At:       time.Now().UTC().Format(time.RFC3339),
			Result:   "read-body-failed",
			Error:    "invalid body",
			Provider: provider,
		})
		http.Error(w, "invalid body", http.StatusBadRequest)
		return
	}
	var payload webhookPayload
	if err := json.NewDecoder(bytes.NewReader(body)).Decode(&payload); err != nil {
		recordCallbackLog(uiCallbackDelivery{
			At:       time.Now().UTC().Format(time.RFC3339),
			Result:   "invalid-json",
			Error:    "invalid body",
			Provider: provider,
		})
		http.Error(w, "invalid body", http.StatusBadRequest)
		return
	}
	if payload.Reference == "" || payload.Status == "" || payload.IdempotencyKey == "" {
		recordCallbackLog(uiCallbackDelivery{
			At:             time.Now().UTC().Format(time.RFC3339),
			Reference:      payload.Reference,
			Provider:       provider,
			Status:         payload.Status,
			Result:         "invalid-payload",
			IdempotencyKey: payload.IdempotencyKey,
			Error:          "reference, status, and idempotency_key are required",
		})
		http.Error(w, "reference, status, and idempotency_key are required", http.StatusBadRequest)
		return
	}
	if err := authorizeWebhookSignature(r, orchestrator, payload.Reference, body); err != nil {
		recordCallbackLog(uiCallbackDelivery{
			At:             time.Now().UTC().Format(time.RFC3339),
			Reference:      payload.Reference,
			Provider:       provider,
			Status:         payload.Status,
			Result:         "failed",
			IdempotencyKey: payload.IdempotencyKey,
			Error:          err.Error(),
		})
		http.Error(w, err.Error(), http.StatusUnauthorized)
		return
	}
	status, attempts, err := orchestrator.ReconcileWebhookWithRetryWithAttempts(payload.Reference, provider, payload.Status, payload.IdempotencyKey)
	if err != nil {
		recordCallbackLog(uiCallbackDelivery{
			At:             time.Now().UTC().Format(time.RFC3339),
			Reference:      payload.Reference,
			Provider:       provider,
			Status:         payload.Status,
			Result:         "failed",
			IdempotencyKey: payload.IdempotencyKey,
			Attempts:       attempts,
			Error:          err.Error(),
		})
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	recordCallbackLog(uiCallbackDelivery{
		At:             time.Now().UTC().Format(time.RFC3339),
		Reference:      payload.Reference,
		Provider:       provider,
		Status:         string(status),
		Result:         "success",
		IdempotencyKey: payload.IdempotencyKey,
		Attempts:       attempts,
	})
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
	if _, err := orchestrator.GetHost(req.ID); err == nil {
		if err := authorizeHostSecret(r, orchestrator, req.ID); err != nil {
			http.Error(w, err.Error(), http.StatusUnauthorized)
			return
		}
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

func isNotFoundErr(err error) bool {
	if err == nil {
		return false
	}
	return strings.Contains(strings.ToLower(err.Error()), "not found")
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

const uiLoginHTML = `<!doctype html>
<html>
<head>
	<meta charset="utf-8"/>
	<title>Login Admin</title>
	<style>
		body { margin: 0; font-family: "Segoe UI", Tahoma, sans-serif; color: #11293f; background: radial-gradient(circle at 12% 0%, #eef3ff 0, #e8f2ff 36%, #f5f7ff 100%); min-height: 100vh; display: grid; place-items: center; padding: 24px; }
		.login-shell { width: min(420px, 100%); background: #fff; border-radius: 16px; padding: 28px; box-shadow: 0 12px 30px rgba(15, 40, 73, 0.15); }
		h1 { margin: 0 0 12px 0; font-size: 26px; color: #13345f; }
		p { margin: 0 0 18px 0; color: #5e6d7f; }
		label { display: block; margin-bottom: 14px; font-size: 14px; color: #214a6f; }
		input { width: 100%; box-sizing: border-box; padding: 10px; margin-top: 6px; border-radius: 10px; border: 1px solid #c7d6e2; }
		button { width: 100%; padding: 10px; border: 0; border-radius: 10px; color: #fff; background: #1f4b7a; font-weight: 700; cursor: pointer; }
		.error { color: #8c1f1f; background: #ffecec; border: 1px solid #ffc8c8; border-radius: 10px; padding: 8px 10px; margin-bottom: 12px; }
		small { color: #6c7d96; display: block; margin-top: 12px; }
	</style>
</head>
<body>
	<div class="login-shell">
		<h1>Login Admin</h1>
		<p>Masukkan password untuk buka dashboard operasi self-hosted.</p>
		{{if .Message}}<div class="error">Login gagal, coba lagi.</div>{{end}}
		<form method="post" action="/ui/login">
			<label>Password
				<input type="password" name="password" autocomplete="current-password" required />
			</label>
			<input type="hidden" name="next" value="{{.Next}}" />
			<button type="submit">Masuk</button>
		</form>
		<small>Default password bisa di-set via env <strong>HOST_RUTEBAYAR_ADMIN_PASSWORD</strong>.</small>
	</div>
</body>
</html>`

const dashboardHTML = `<!doctype html>
<html>
<head>
	<meta charset="utf-8"/>
	<title>host-rutebayar self-hosted</title>
	<style>
		body { font-family: "Segoe UI", Tahoma, sans-serif; margin: 0; color: #11293f; background: radial-gradient(circle at 12% 0%, #eef3ff 0, #e8f2ff 36%, #f5f7ff 100%); }
		a { color: #114b8a; }
		h1, h2, h3 { margin-top: 0; font-family: Georgia, "Times New Roman", serif; letter-spacing: -0.02em; }
		.section { background: #fff; border-radius: 12px; padding: 16px; margin-bottom: 20px; box-shadow: 0 10px 26px rgba(28, 46, 74, 0.08); }
		table { border-collapse: collapse; width: 100%; margin-top: 8px; }
		th, td { border: 1px solid #cfdee6; padding: 8px; text-align: left; vertical-align: top; }
		th { background: #16385f; color: #fff; }
		.admin-shell { display: grid; grid-template-columns: 270px minmax(0, 1fr); min-height: 100vh; }
		.sidebar { position: sticky; top: 0; height: 100vh; overflow-y: auto; padding: 24px 20px; background: linear-gradient(160deg, #13345f, #183e73); color: #ecf3ff; }
		.sidebar h2 { margin: 0 0 18px 0; font-size: 18px; letter-spacing: 0.3px; }
		.sidebar nav { display: flex; flex-direction: column; gap: 8px; }
		.sidebar a { color: #eff5ff; text-decoration: none; font-size: 14px; border-radius: 9px; padding: 10px 12px; }
		.sidebar a:hover { background: rgba(255, 255, 255, 0.12); }
		.sidebar .active { background: rgba(255, 255, 255, 0.2); font-weight: 700; }
		.sidebar .muted { margin-top: 12px; opacity: 0.85; font-size: 12px; }
		.content { padding: 16px 20px 24px 20px; overflow-x: auto; }
		.top-actions { display: flex; gap: 12px; align-items: center; justify-content: space-between; flex-wrap: wrap; }
		.subtle { color: #6f7f95; font-size: 14px; }
		.eyebrow { margin: 0 0 8px 0; text-transform: uppercase; letter-spacing: 0.16em; font-size: 11px; color: #5a6b86; font-weight: 700; }
		.hero { display: flex; gap: 20px; justify-content: space-between; align-items: flex-start; padding: 24px; margin-bottom: 18px; border-radius: 18px; background: linear-gradient(135deg, rgba(19, 52, 95, 0.96), rgba(34, 92, 159, 0.92)); color: #eff5ff; box-shadow: 0 20px 34px rgba(16, 34, 59, 0.2); }
		.hero h1 { margin-bottom: 10px; color: #fff; font-size: 32px; }
		.hero .subtle { color: rgba(239, 245, 255, 0.82); max-width: 760px; }
		.hero-actions { display: flex; gap: 10px; flex-wrap: wrap; justify-content: flex-end; }
		.button-link, .ghost-link { display: inline-flex; align-items: center; justify-content: center; gap: 8px; padding: 10px 14px; border-radius: 10px; text-decoration: none; font-weight: 700; }
		.button-link { background: #fff; color: #14335a; box-shadow: 0 8px 18px rgba(7, 21, 39, 0.18); }
		.ghost-link { color: #eff5ff; border: 1px solid rgba(255, 255, 255, 0.2); background: rgba(255, 255, 255, 0.08); }
		.stats { display: grid; grid-template-columns: repeat(auto-fit, minmax(170px, 1fr)); gap: 12px; margin: 0 0 18px 0; }
		.stat { padding: 16px; border-radius: 16px; background: #fff; border: 1px solid #d5e0ec; box-shadow: 0 10px 26px rgba(28, 46, 74, 0.06); }
		.stat strong { display: block; font-size: 28px; line-height: 1; margin-top: 8px; color: #16385f; }
		.stat span { color: #5d6d84; font-size: 13px; }
		.table-wrap { overflow-x: auto; margin-top: 8px; border-radius: 10px; }
		.pill { display: inline-flex; align-items: center; border-radius: 999px; padding: 4px 10px; background: #edf4ff; color: #204b83; font-size: 12px; font-weight: 700; }
		.grid { display: grid; grid-template-columns: repeat(auto-fit, minmax(280px, 1fr)); gap: 16px; }
		input, textarea, select, button { width: 100%; box-sizing: border-box; padding: 8px; margin-top: 6px; border-radius: 8px; border: 1px solid #bfd0dd; }
		textarea { min-height: 72px; font-family: monospace; }
		button { background: #204b83; color: #fff; font-weight: 700; cursor: pointer; border: 0; }
		button:hover { background: #14335a; }
		.small-btn { width: auto; min-width: 86px; padding: 6px 10px; }
		.msg { margin-top: 10px; padding: 8px 10px; border-radius: 8px; display: none; }
		.msg.ok { background: #eaffea; border: 1px solid #87d28f; color: #1f5a1f; }
		.msg.err { background: #ffe9e9; border: 1px solid #ef9494; color: #7c1b1b; }
		@media (max-width: 1024px) {
			.admin-shell { grid-template-columns: 1fr; }
			.sidebar { position: static; height: auto; display: flex; flex-wrap: wrap; gap: 16px; align-items: flex-start; }
			.sidebar nav { width: 100%; display: flex; flex-wrap: wrap; }
			.sidebar nav a { padding: 8px 12px; }
			.hero { flex-direction: column; }
			.hero-actions { justify-content: flex-start; }
		}
	</style>
	<script>
		function splitCSV(value) {
			return value.split(",").map((s) => s.trim()).filter((s) => s.length > 0);
		}
		function showMessage(message, isOk) {
			const el = document.getElementById("action-result");
			el.textContent = message;
			el.className = "msg " + (isOk ? "ok" : "err");
			el.style.display = "block";
		}
		async function postJSON(url, body, headers = {}) {
			const response = await fetch(url, {
				method: "POST",
				headers: Object.assign({ "Content-Type": "application/json" }, headers),
				body: JSON.stringify(body),
			});
			const text = await response.text();
			let payload;
			try {
				payload = JSON.parse(text);
			} catch (e) {
				payload = text;
			}
			if (!response.ok) {
				throw new Error(typeof payload === "string" ? payload : (payload.message || payload.error || JSON.stringify(payload)));
			}
			return payload;
		}
		function syncProductOptions() {
			const hostID = document.getElementById("test-host-id").value;
			const productSelect = document.getElementById("test-product-id");
			let firstAvailable = "";
			for (const option of productSelect.options) {
				const matchHost = !option.dataset.host || option.dataset.host === hostID;
				option.hidden = !matchHost;
				if (matchHost && firstAvailable === "" && option.value) {
					firstAvailable = option.value;
				}
			}
			if (firstAvailable && !productSelect.value) {
				productSelect.value = firstAvailable;
			}
			if (firstAvailable && productSelect.value && productSelect.selectedOptions[0]?.hidden) {
				productSelect.value = firstAvailable;
			}
		}
		async function submitHost(event) {
			event.preventDefault();
			try {
				const payload = {
					id: document.getElementById("host-id").value.trim(),
					name: document.getElementById("host-name").value.trim(),
					notification_key: document.getElementById("host-notification-key").value.trim(),
					host_secret: document.getElementById("host-secret").value.trim(),
					webhook_secret: document.getElementById("host-webhook-secret").value.trim(),
					callback_urls: splitCSV(document.getElementById("host-callback-urls").value),
					callback_allowlist: splitCSV(document.getElementById("host-callback-allowlist").value),
				};
				await postJSON("/register/host", payload);
				showMessage("Host berhasil disimpan. Halaman dimuat ulang.", true);
				setTimeout(() => location.reload(), 600);
			} catch (err) {
				showMessage("Gagal simpan host: " + err.message, false);
			}
		}
		async function submitProduct(event) {
			event.preventDefault();
			try {
				const hostID = document.getElementById("product-host-id").value;
				const payload = {
					id: document.getElementById("product-id").value.trim(),
					host_id: hostID,
					name: document.getElementById("product-name").value.trim(),
					sku: document.getElementById("product-sku").value.trim(),
					price: Number(document.getElementById("product-price").value),
					is_active: document.getElementById("product-active").checked,
				};
				const metaRaw = document.getElementById("product-meta").value.trim();
				const overrideRaw = document.getElementById("product-fee-override").value.trim();
				if (metaRaw) {
					payload.meta = JSON.parse(metaRaw);
				} else {
					payload.meta = {};
				}
				if (overrideRaw) {
					payload.fee_policy_override = JSON.parse(overrideRaw);
				}
				await postJSON("/register/product", payload, {
					"X-Host-Secret": document.getElementById("product-host-secret").value,
				});
				showMessage("Produk berhasil disimpan. Halaman dimuat ulang.", true);
				setTimeout(() => location.reload(), 600);
			} catch (err) {
				showMessage("Gagal simpan produk: " + err.message, false);
			}
		}
		async function submitProviderAccount(event) {
			event.preventDefault();
			try {
				const payload = {
					host_id: document.getElementById("provider-host-id").value,
					provider: document.getElementById("provider-name").value.trim(),
					env: document.getElementById("provider-env").value.trim() || "sandbox",
					credentials_hash: document.getElementById("provider-credentials").value.trim(),
					public_config: {},
				};
				const publicConfig = document.getElementById("provider-config").value.trim();
				if (publicConfig) {
					payload.public_config = JSON.parse(publicConfig);
				}
				await postJSON("/register/provider-account", payload, {
					"X-Host-Secret": document.getElementById("provider-host-secret").value,
				});
				showMessage("Provider account berhasil disimpan. Halaman dimuat ulang.", true);
				setTimeout(() => location.reload(), 600);
			} catch (err) {
				showMessage("Gagal simpan provider account: " + err.message, false);
			}
		}
		async function submitHostPolicy(event) {
			event.preventDefault();
			try {
				const payload = {
					host_id: document.getElementById("policy-host-id").value,
					fee_policy: {
						type: document.getElementById("policy-type").value.trim() || "percent",
						value: Number(document.getElementById("policy-value").value),
						currency: document.getElementById("policy-currency").value.trim() || "IDR",
						rounding: document.getElementById("policy-rounding").value.trim() || "nearest",
						policy_version: document.getElementById("policy-version").value.trim() || "v1",
					},
				};
				const minFeeRaw = document.getElementById("policy-min-fee").value.trim();
				const maxFeeRaw = document.getElementById("policy-max-fee").value.trim();
				if (minFeeRaw) payload.fee_policy.min_fee = Number(minFeeRaw);
				if (maxFeeRaw) payload.fee_policy.max_fee = Number(maxFeeRaw);
				await postJSON("/register/host-policy", payload, {
					"X-Host-Secret": document.getElementById("policy-host-secret").value,
				});
				showMessage("Host policy berhasil disimpan.", true);
				setTimeout(() => location.reload(), 600);
			} catch (err) {
				showMessage("Gagal simpan host policy: " + err.message, false);
			}
		}
		async function seedDemo(event) {
			event.preventDefault();
			try {
				const response = await postJSON("/admin/demo-seed", {});
				document.getElementById("seed-output").textContent = JSON.stringify(response, null, 2);
				showMessage("Data demo berhasil dibuat. Halaman dimuat ulang.", true);
				setTimeout(() => location.reload(), 600);
			} catch (err) {
				showMessage("Seed gagal: " + err.message, false);
			}
		}
		async function createTestPayment(event) {
			event.preventDefault();
			try {
				const hostID = document.getElementById("test-host-id").value;
				const payload = {
					host_id: hostID,
					product_id: document.getElementById("test-product-id").value,
					buyer_ref: document.getElementById("test-buyer-ref").value.trim(),
					env: document.getElementById("test-env").value.trim() || "sandbox",
				};
				const response = await postJSON("/admin/test-payment", payload);
				const message = "Reference: " + response.reference + "\nCheckout: " + response.checkout_url;
				document.getElementById("test-output").textContent = message;
				showMessage("Test payment berhasil dibuat.", true);
			} catch (err) {
				showMessage("Test payment gagal: " + err.message, false);
			}
		}
		window.addEventListener("DOMContentLoaded", () => {
			const hostSelector = document.getElementById("test-host-id");
			if (hostSelector) {
				hostSelector.addEventListener("change", syncProductOptions);
				syncProductOptions();
			}
		});
	</script>
</head>
<body>
	<div class="admin-shell">
		<aside class="sidebar">
			<h2>Host RuteBayar</h2>
			<p class="muted">Dashboard Admin</p>
			<nav>
				<a href="/ui" class="active">🏠 Dashboard</a>
				<a href="/ui/callbacks">🔁 Callback Monitor</a>
				<a href="/ui#hosts">📋 Hosts</a>
				<a href="/ui#products">🧩 Products</a>
				<a href="/ui#orders">🧾 Orders</a>
				<a href="/ui/logout">🚪 Logout</a>
			</nav>
			<p class="muted">Akses jalur ini untuk operasional lokal dan pemantauan checkout.</p>
		</aside>
		<main class="content">
			<div class="hero">
				<div>
					<p class="eyebrow">Operator workspace</p>
					<h1>host-rutebayar self-hosted</h1>
					<p class="subtle">Satu panel untuk registrasi host, produk, provider account, policy, dan smoke test tanpa harus pindah-pindah halaman.</p>
				</div>
				<div class="hero-actions">
					<a class="button-link" href="/ui/callbacks">Lihat callback monitor</a>
					<a class="ghost-link" href="#orders">Loncat ke orders</a>
				</div>
			</div>
			<div class="stats">
				<div class="stat">
					<span>Hosts aktif</span>
					<strong>{{len .Hosts}}</strong>
				</div>
				<div class="stat">
					<span>Products</span>
					<strong>{{len .Products}}</strong>
				</div>
				<div class="stat">
					<span>Orders</span>
					<strong>{{len .Orders}}</strong>
				</div>
				<div class="stat">
					<span>Fast path</span>
					<strong><span class="pill">Seed + Test</span></strong>
				</div>
			</div>
			<div id="action-result" class="msg"></div>
			<div class="section">
				<h2>1) Quick bootstrap</h2>
				<p class="subtle">Buat data demo sekali klik untuk cek alur end-to-end dengan cepat.</p>
				<button type="button" onclick="seedDemo(event)" class="small-btn">Seed demo data</button>
				<div class="table-wrap"><pre id="seed-output"></pre></div>
			</div>
			<div class="grid">
				<section class="section">
					<h2>2) Register Host</h2>
					<form onsubmit="submitHost(event)">
						<label>ID
							<input id="host-id" required />
						</label>
						<label>Nama
							<input id="host-name" required />
						</label>
						<label>Notification Key
							<input id="host-notification-key" required />
						</label>
						<label>Host Secret
							<input id="host-secret" required />
						</label>
						<label>Webhook Secret
							<input id="host-webhook-secret" required />
						</label>
						<label>Callback URLs (pisahkan dengan koma)
							<textarea id="host-callback-urls">https://example.com/callback</textarea>
						</label>
						<label>Callback Allowlist (pisahkan dengan koma)
							<textarea id="host-callback-allowlist">https://example.com</textarea>
						</label>
						<button type="submit">Create Host</button>
					</form>
				</section>
				<section class="section">
					<h2>3) Register Product</h2>
					<form onsubmit="submitProduct(event)">
						<label>Host
							<select id="product-host-id" required>
								<option value="">Pilih Host</option>
								{{range .Hosts}}<option value="{{.ID}}">{{.ID}}</option>{{end}}
							</select>
						</label>
						<label>Host Secret
							<input id="product-host-secret" type="password" required />
						</label>
						<label>Product ID
							<input id="product-id" required />
						</label>
						<label>Nama Produk
							<input id="product-name" required />
						</label>
						<label>SKU
							<input id="product-sku" required />
						</label>
						<label>Harga (integer)
							<input id="product-price" type="number" min="0" step="1" required />
						</label>
						<label>Meta JSON (opsional)
							<textarea id="product-meta">{}</textarea>
						</label>
						<label>Fee Policy Override JSON (opsional)
							<textarea id="product-fee-override"></textarea>
						</label>
						<label style="display:flex; align-items:center; gap:8px;">
							<input id="product-active" type="checkbox" checked />
							<span>Is Active</span>
						</label>
						<button type="submit">Create Product</button>
					</form>
				</section>
			</div>
			<div class="grid">
				<section class="section">
					<h2>4) Register Provider Account</h2>
					<form onsubmit="submitProviderAccount(event)">
						<label>Host
							<select id="provider-host-id" required>
								<option value="">Pilih Host</option>
								{{range .Hosts}}<option value="{{.ID}}">{{.ID}}</option>{{end}}
							</select>
						</label>
						<label>Host Secret
							<input id="provider-host-secret" type="password" required />
						</label>
						<label>Provider
							<input id="provider-name" value="midtrans" required />
						</label>
						<label>Environment
							<input id="provider-env" value="sandbox" />
						</label>
						<label>Credentials Hash
							<input id="provider-credentials" required />
						</label>
						<label>Public Config JSON (opsional)
							<textarea id="provider-config"></textarea>
						</label>
						<button type="submit">Register Account</button>
					</form>
				</section>
				<section class="section">
					<h2>5) Host Policy</h2>
					<form onsubmit="submitHostPolicy(event)">
						<label>Host
							<select id="policy-host-id" required>
								<option value="">Pilih Host</option>
								{{range .Hosts}}<option value="{{.ID}}">{{.ID}}</option>{{end}}
							</select>
						</label>
						<label>Host Secret
							<input id="policy-host-secret" type="password" required />
						</label>
						<label>Type
							<select id="policy-type">
								<option value="percent">percent</option>
								<option value="fixed">fixed</option>
								<option value="free">free</option>
							</select>
						</label>
						<label>Value
							<input id="policy-value" type="number" step="0.1" value="2" required />
						</label>
						<label>Currency
							<input id="policy-currency" value="IDR" />
						</label>
						<label>Rounding
							<input id="policy-rounding" value="nearest" />
						</label>
						<label>Min Fee (opsional)
							<input id="policy-min-fee" type="number" />
						</label>
						<label>Max Fee (opsional)
							<input id="policy-max-fee" type="number" />
						</label>
						<label>Policy Version
							<input id="policy-version" value="v1" />
						</label>
						<button type="submit">Set Policy</button>
					</form>
				</section>
			</div>
			<div class="section">
				<h2>6) Test Payment</h2>
				<p class="subtle">Pilih host dan product yang matching supaya payload test tidak nyasar.</p>
				<form onsubmit="createTestPayment(event)">
					<div class="grid">
						<label>Host
							<select id="test-host-id" required>
								<option value="">Pilih Host</option>
								{{range .Hosts}}<option value="{{.ID}}">{{.ID}}</option>{{end}}
							</select>
						</label>
						<label>Product
							<select id="test-product-id" required>
								<option value="">Pilih Product</option>
								{{range .Products}}<option value="{{.ID}}" data-host="{{.HostID}}">{{.ID}}</option>{{end}}
							</select>
						</label>
						<label>Env
							<input id="test-env" value="sandbox" />
						</label>
						<label>Buyer Ref
							<input id="test-buyer-ref" />
						</label>
					</div>
					<button type="submit">Create test payment</button>
				</form>
				<div class="table-wrap"><pre id="test-output"></pre></div>
			</div>
			<div id="hosts" class="section">
				<h2>Hosts</h2>
				<div class="table-wrap">
					<table>
						<tr><th>ID</th><th>Nama</th><th>Callback URL</th><th>Allowlist</th></tr>
						{{range .Hosts}}
						<tr>
							<td><a href="/ui/host/{{.ID}}">{{.ID}}</a></td>
							<td>{{.Name}}</td>
							<td><pre>{{range .CallbackURLs}}{{.}} {{end}}</pre></td>
							<td><pre>{{range .CallbackAllowlist}}{{.}} {{end}}</pre></td>
						</tr>
						{{else}}
						<tr><td colspan="4">Belum ada host terdaftar.</td></tr>
						{{end}}
					</table>
				</div>
			</div>
			<div id="products" class="section">
				<h2>Products</h2>
				<div class="table-wrap">
					<table>
						<tr><th>ID</th><th>Host ID</th><th>Nama</th><th>SKU</th><th>Harga</th><th>Active</th><th>Policy Override</th></tr>
						{{range .Products}}
						<tr>
							<td><a href="/ui/product/{{.ID}}">{{.ID}}</a></td>
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
			</div>
			<div id="orders" class="section">
				<h2>Orders</h2>
				<div class="table-wrap">
					<table>
						<tr><th>Reference</th><th>Status</th><th>Host</th><th>Product</th><th>Provider</th><th>Gross</th><th>Host Fee</th><th>Net</th><th>Checkout URL</th></tr>
						{{range .Orders}}
						<tr>
							<td><a href="/ui/order/{{.Reference}}">{{.Reference}}</a></td>
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
			</div>
		</main>
	</div>
</body>
</html>`

const uiHostHTML = `<!doctype html>
<html>
<head>
	<meta charset="utf-8"/>
	<title>Host {{.Host.ID}}</title>
	<style>
		:root { color-scheme: light; }
		body { margin: 0; font-family: "Segoe UI", Tahoma, sans-serif; color: #11293f; background: #f2f8ff; }
		a { color: #1f4a91; }
		.admin-shell { display: grid; grid-template-columns: 270px minmax(0, 1fr); min-height: 100vh; }
		.sidebar { position: sticky; top: 0; height: 100vh; overflow-y: auto; padding: 24px 20px; background: linear-gradient(160deg, #13345f, #183e73); color: #ecf3ff; }
		.sidebar h2 { margin: 0 0 18px 0; font-size: 18px; letter-spacing: 0.3px; }
		.sidebar nav { display: flex; flex-direction: column; gap: 8px; }
		.sidebar a { color: #eff5ff; text-decoration: none; font-size: 14px; border-radius: 9px; padding: 10px 12px; }
		.sidebar a:hover { background: rgba(255, 255, 255, 0.12); }
		.sidebar .active { background: rgba(255, 255, 255, 0.2); font-weight: 700; }
		.sidebar .muted { margin-top: 12px; opacity: 0.85; font-size: 12px; }
		.content { padding: 16px 20px 24px 20px; overflow-x: auto; }
		.section { background: #fff; border-radius: 12px; padding: 16px; margin-bottom: 20px; box-shadow: 0 8px 24px rgba(2, 53, 110, 0.08); }
		.section h2 { margin-top: 0; }
		.back-link { display: inline-block; margin-bottom: 12px; }
		table { border-collapse: collapse; width: 100%; margin-bottom: 20px; }
		td, th { border: 1px solid #c4d0dc; padding: 8px; text-align: left; }
		th { background: #1f3c5d; color: #fff; }
		pre { background: #f8fcff; padding: 8px; border: 1px solid #d6e4f0; }
		.hero { padding: 24px; margin-bottom: 16px; border-radius: 18px; background: linear-gradient(135deg, rgba(19, 52, 95, 0.96), rgba(34, 92, 159, 0.92)); color: #eff5ff; box-shadow: 0 20px 34px rgba(16, 34, 59, 0.2); }
		.hero h1 { margin: 0 0 8px 0; color: #fff; }
		.subtle { color: #dce8f8; }
		.grid { display: grid; grid-template-columns: repeat(auto-fit, minmax(220px, 1fr)); gap: 12px; }
		.grid .card { background: #fff; border: 1px solid #d7e3ef; border-radius: 12px; padding: 14px; }
		@media (max-width: 1024px) {
			.admin-shell { grid-template-columns: 1fr; }
			.sidebar { position: static; height: auto; display: flex; flex-wrap: wrap; gap: 16px; }
			.sidebar nav { width: 100%; display: flex; flex-wrap: wrap; }
			.sidebar nav a { padding: 8px 12px; }
		}
	</style>
</head>
<body>
	<div class="admin-shell">
		<aside class="sidebar">
			<h2>Host RuteBayar</h2>
			<p class="muted">Dashboard Host Detail</p>
			<nav>
				<a href="/ui">🏠 Dashboard</a>
				<a href="/ui/callbacks">🔁 Callback Monitor</a>
				<a href="/ui#hosts" class="active">📋 Host / Produk / Orders</a>
				<a href="/ui/logout">🚪 Logout</a>
			</nav>
			<p class="muted">Semua aksi operasional tetap tercatat dari halaman dashboard.</p>
		</aside>
		<main class="content">
			<div class="hero">
				<div>
					<p class="subtle">Host detail</p>
					<h1>{{.Host.ID}}</h1>
					<p class="subtle">Informasi credential dan data operasional host.</p>
				</div>
				<a href="/ui" style="text-decoration:none;color:#fff;border:1px solid rgba(255,255,255,0.2);padding:9px 14px;border-radius:10px;">← Kembali ke Dashboard</a>
			</div>
			<div class="grid">
				<div class="card">
					<strong>Nama host</strong><div>{{.Host.Name}}</div>
				</div>
				<div class="card">
					<strong>Total produk</strong><div>{{len .Products}}</div>
				</div>
				<div class="card">
					<strong>Total order</strong><div>{{len .Orders}}</div>
				</div>
			</div>
			<div class="section">
				<h2>Data host</h2>
				<table>
					<tr><th>Field</th><th>Value</th></tr>
					<tr><td>ID</td><td>{{.Host.ID}}</td></tr>
					<tr><td>Nama</td><td>{{.Host.Name}}</td></tr>
					<tr><td>Notification Key</td><td>{{if .Host.NotificationKey}}configured{{else}}-{{end}}</td></tr>
					<tr><td>Callback URLs</td><td><pre>{{range .Host.CallbackURLs}}{{.}} {{end}}</pre></td></tr>
					<tr><td>Callback Allowlist</td><td><pre>{{range .Host.CallbackAllowlist}}{{.}} {{end}}</pre></td></tr>
					<tr><td>Host Secret</td><td>{{if .Host.HostSecret}}configured{{else}}-{{end}}</td></tr>
					<tr><td>Webhook Secret</td><td>{{if .Host.WebhookSecret}}configured{{else}}-{{end}}</td></tr>
				</table>
			</div>
			<div class="section">
				<h2>Produk</h2>
				<table>
					<tr><th>ID</th><th>Nama</th><th>SKU</th><th>Harga</th><th>Active</th></tr>
					{{range .Products}}
					<tr>
						<td><a href="/ui/product/{{.ID}}">{{.ID}}</a></td>
						<td>{{.Name}}</td>
						<td>{{.SKU}}</td>
						<td>{{.Price}}</td>
						<td>{{.IsActive}}</td>
					</tr>
					{{else}}
					<tr><td colspan="5">Belum ada produk.</td></tr>
					{{end}}
				</table>
			</div>
			<div class="section">
				<h2>Orders</h2>
				<table>
					<tr><th>Reference</th><th>Status</th><th>Produk</th><th>Provider</th><th>Gross</th><th>Host Fee</th><th>Net</th></tr>
					{{range .Orders}}
					<tr>
						<td><a href="/ui/order/{{.Reference}}">{{.Reference}}</a></td>
						<td>{{.Status}}</td>
						<td>{{.ProductID}}</td>
						<td>{{.Provider}}</td>
						<td>{{.GrossAmount}}</td>
						<td>{{.HostFeeAmount}}</td>
						<td>{{.NetAmount}}</td>
					</tr>
					{{else}}
					<tr><td colspan="7">Belum ada order.</td></tr>
					{{end}}
				</table>
			</div>
			<div class="section">
				<h2>Provider Accounts</h2>
				<table>
					<tr><th>Provider</th><th>Env</th><th>Credentials Hash</th><th>Public Config</th></tr>
					{{range .Accounts}}
					<tr>
						<td>{{.Provider}}</td>
						<td>{{.Env}}</td>
						<td><pre>{{.CredentialsHash}}</pre></td>
						<td><pre>{{printf "%#v" .PublicConfig}}</pre></td>
					</tr>
					{{else}}
					<tr><td colspan="4">Belum ada provider account.</td></tr>
					{{end}}
				</table>
			</div>
		</main>
	</div>
</body>
</html>`

const uiProductHTML = `<!doctype html>
<html>
<head>
	<meta charset="utf-8"/>
	<title>Product {{.Product.ID}}</title>
	<style>
		:root { color-scheme: light; }
		body { margin: 0; font-family: "Segoe UI", Tahoma, sans-serif; color: #11293f; background: #f2f8ff; }
		a { color: #1f4a91; }
		.admin-shell { display: grid; grid-template-columns: 270px minmax(0, 1fr); min-height: 100vh; }
		.sidebar { position: sticky; top: 0; height: 100vh; overflow-y: auto; padding: 24px 20px; background: linear-gradient(160deg, #13345f, #183e73); color: #ecf3ff; }
		.sidebar h2 { margin: 0 0 18px 0; font-size: 18px; letter-spacing: 0.3px; }
		.sidebar nav { display: flex; flex-direction: column; gap: 8px; }
		.sidebar a { color: #eff5ff; text-decoration: none; font-size: 14px; border-radius: 9px; padding: 10px 12px; }
		.sidebar a:hover { background: rgba(255, 255, 255, 0.12); }
		.sidebar .active { background: rgba(255, 255, 255, 0.2); font-weight: 700; }
		.sidebar .muted { margin-top: 12px; opacity: 0.85; font-size: 12px; }
		.content { padding: 16px 20px 24px 20px; overflow-x: auto; }
		.section { background: #fff; border-radius: 12px; padding: 16px; margin-bottom: 20px; box-shadow: 0 8px 24px rgba(2, 53, 110, 0.08); }
		table { border-collapse: collapse; width: 100%; margin-bottom: 20px; }
		td, th { border: 1px solid #c4d0dc; padding: 8px; text-align: left; }
		th { background: #1f3c5d; color: #fff; }
		pre { background: #f8fcff; padding: 8px; border: 1px solid #d6e4f0; }
		.hero { padding: 24px; margin-bottom: 16px; border-radius: 18px; background: linear-gradient(135deg, rgba(19, 52, 95, 0.96), rgba(34, 92, 159, 0.92)); color: #eff5ff; box-shadow: 0 20px 34px rgba(16, 34, 59, 0.2); }
		.hero h1 { margin: 0 0 8px 0; color: #fff; }
		.subtle { color: #dce8f8; }
		@media (max-width: 1024px) {
			.admin-shell { grid-template-columns: 1fr; }
			.sidebar { position: static; height: auto; display: flex; flex-wrap: wrap; gap: 16px; }
			.sidebar nav { width: 100%; display: flex; flex-wrap: wrap; }
			.sidebar nav a { padding: 8px 12px; }
		}
	</style>
</head>
<body>
	<div class="admin-shell">
		<aside class="sidebar">
			<h2>Host RuteBayar</h2>
			<p class="muted">Dashboard Product Detail</p>
			<nav>
				<a href="/ui">🏠 Dashboard</a>
				<a href="/ui/callbacks">🔁 Callback Monitor</a>
				<a href="/ui#products" class="active">🧩 Produk</a>
				<a href="/ui/logout">🚪 Logout</a>
			</nav>
		</aside>
		<main class="content">
			<div class="hero">
				<p class="subtle">Product detail</p>
				<h1>{{.Product.ID}}</h1>
				<p class="subtle">Lengkap dengan relasi host dan konfigurasi fee override.</p>
			</div>
			<div class="section">
				<table>
					<tr><th>Field</th><th>Value</th></tr>
					<tr><td>ID</td><td>{{.Product.ID}}</td></tr>
					<tr><td>Host</td><td><a href="/ui/host/{{.Product.HostID}}">{{.Product.HostID}}</a></td></tr>
					<tr><td>Nama</td><td>{{.Product.Name}}</td></tr>
					<tr><td>SKU</td><td>{{.Product.SKU}}</td></tr>
					<tr><td>Harga</td><td>{{.Product.Price}}</td></tr>
					<tr><td>Active</td><td>{{.Product.IsActive}}</td></tr>
					<tr><td>Policy Override</td><td>{{if .Product.FeePolicyOverride}}yes{{else}}no{{end}}</td></tr>
				</table>
			</div>
			{{if .Product.FeePolicyOverride}}
			<div class="section">
				<h2>Policy override</h2>
				<pre>{{printf "%#v" .Product.FeePolicyOverride}}</pre>
			</div>
			{{end}}
		</main>
	</div>
</body>
</html>`

const uiOrderHTML = `<!doctype html>
<html>
<head>
	<meta charset="utf-8"/>
	<title>Order {{.Order.Reference}}</title>
	<style>
		:root { color-scheme: light; }
		body { margin: 0; font-family: "Segoe UI", Tahoma, sans-serif; color: #11293f; background: #f2f8ff; }
		a { color: #1f4a91; }
		.admin-shell { display: grid; grid-template-columns: 270px minmax(0, 1fr); min-height: 100vh; }
		.sidebar { position: sticky; top: 0; height: 100vh; overflow-y: auto; padding: 24px 20px; background: linear-gradient(160deg, #13345f, #183e73); color: #ecf3ff; }
		.sidebar h2 { margin: 0 0 18px 0; font-size: 18px; letter-spacing: 0.3px; }
		.sidebar nav { display: flex; flex-direction: column; gap: 8px; }
		.sidebar a { color: #eff5ff; text-decoration: none; font-size: 14px; border-radius: 9px; padding: 10px 12px; }
		.sidebar a:hover { background: rgba(255, 255, 255, 0.12); }
		.sidebar .active { background: rgba(255, 255, 255, 0.2); font-weight: 700; }
		.sidebar .muted { margin-top: 12px; opacity: 0.85; font-size: 12px; }
		.content { padding: 16px 20px 24px 20px; overflow-x: auto; }
		.section { background: #fff; border-radius: 12px; padding: 16px; margin-bottom: 20px; box-shadow: 0 8px 24px rgba(2, 53, 110, 0.08); }
		table { border-collapse: collapse; width: 100%; margin-bottom: 20px; }
		td, th { border: 1px solid #c4d0dc; padding: 8px; text-align: left; }
		th { background: #1f3c5d; color: #fff; }
		pre { background: #f8fcff; padding: 8px; border: 1px solid #d6e4f0; }
		.hero { padding: 24px; margin-bottom: 16px; border-radius: 18px; background: linear-gradient(135deg, rgba(19, 52, 95, 0.96), rgba(34, 92, 159, 0.92)); color: #eff5ff; box-shadow: 0 20px 34px rgba(16, 34, 59, 0.2); }
		.hero h1 { margin: 0 0 8px 0; color: #fff; }
		.subtle { color: #dce8f8; }
		@media (max-width: 1024px) {
			.admin-shell { grid-template-columns: 1fr; }
			.sidebar { position: static; height: auto; display: flex; flex-wrap: wrap; gap: 16px; }
			.sidebar nav { width: 100%; display: flex; flex-wrap: wrap; }
			.sidebar nav a { padding: 8px 12px; }
		}
	</style>
</head>
<body>
	<div class="admin-shell">
		<aside class="sidebar">
			<h2>Host RuteBayar</h2>
			<p class="muted">Dashboard Order Detail</p>
			<nav>
				<a href="/ui">🏠 Dashboard</a>
				<a href="/ui/callbacks">🔁 Callback Monitor</a>
				<a href="/ui#orders" class="active">🧾 Orders</a>
				<a href="/ui/logout">🚪 Logout</a>
			</nav>
		</aside>
		<main class="content">
			<div class="hero">
				<p class="subtle">Order detail</p>
				<h1>{{.Order.Reference}}</h1>
				<p class="subtle">Riwayat status dan data settlement untuk transaksi ini.</p>
			</div>
			<div class="section">
				<h2>Informasi order</h2>
				<table>
					<tr><th>Field</th><th>Value</th></tr>
					<tr><td>Status</td><td>{{.Order.Status}}</td></tr>
					<tr><td>Host</td><td><a href="/ui/host/{{.Order.HostID}}">{{.Order.HostID}}</a></td></tr>
					<tr><td>Produk</td><td><a href="/ui/product/{{.Order.ProductID}}">{{.Order.ProductID}}</a></td></tr>
					<tr><td>Provider</td><td>{{.Order.Provider}}</td></tr>
					<tr><td>Env</td><td>{{.Order.Env}}</td></tr>
					<tr><td>Reference</td><td>{{.Order.Reference}}</td></tr>
					<tr><td>Gross</td><td>{{.Order.GrossAmount}}</td></tr>
					<tr><td>Host Fee</td><td>{{.Order.HostFeeAmount}}</td></tr>
					<tr><td>Provider Fee</td><td>{{.Order.ProviderFeeAmount}}</td></tr>
					<tr><td>Net</td><td>{{.Order.NetAmount}}</td></tr>
					<tr><td>Checkout URL</td><td><a href="{{.Order.ProviderCheckoutURL}}">{{if .Order.ProviderCheckoutURL}}open{{else}}-{{end}}</a></td></tr>
				</table>
			</div>
			<div class="section">
				<h2>Ledger</h2>
				{{if .HasLedger}}
				<table>
					<tr><th>Field</th><th>Value</th></tr>
					<tr><td>Policy Checksum</td><td>{{.Ledger.PolicyChecksum}}</td></tr>
					<tr><td>Gross Amount</td><td>{{.Ledger.GrossAmount}}</td></tr>
					<tr><td>Host Fee</td><td>{{.Ledger.HostFeeAmount}}</td></tr>
					<tr><td>Provider Fee</td><td>{{.Ledger.ProviderFeeAmount}}</td></tr>
					<tr><td>Net Amount</td><td>{{.Ledger.NetAmount}}</td></tr>
					<tr><td>Idempotency Key</td><td>{{.Ledger.IdempotencyKey}}</td></tr>
				</table>
				{{else}}
				<p>Ledger not found yet.</p>
				{{end}}
			</div>
		</main>
	</div>
</body>
</html>`

const uiCallbacksHTML = `<!doctype html>
<html>
<head>
	<meta charset="utf-8"/>
	<title>Callback monitor</title>
	<style>
		body { font-family: "Segoe UI", Tahoma, sans-serif; margin: 0; color: #11293f; background: #f2f8ff; }
		.admin-shell { display: grid; grid-template-columns: 270px minmax(0, 1fr); min-height: 100vh; }
		.sidebar { position: sticky; top: 0; height: 100vh; overflow-y: auto; padding: 24px 20px; background: linear-gradient(160deg, #13345f, #183e73); color: #ecf3ff; }
		.sidebar h2 { margin: 0 0 18px 0; font-size: 18px; letter-spacing: 0.3px; }
		.sidebar nav { display: flex; flex-direction: column; gap: 8px; }
		.sidebar a { color: #eff5ff; text-decoration: none; font-size: 14px; border-radius: 9px; padding: 10px 12px; }
		.sidebar a:hover { background: rgba(255, 255, 255, 0.12); }
		.sidebar .active { background: rgba(255, 255, 255, 0.2); font-weight: 700; }
		.sidebar .muted { margin-top: 12px; opacity: 0.85; font-size: 12px; }
		.content { padding: 16px 20px; overflow-x: auto; }
		.eyebrow { margin: 0 0 8px 0; text-transform: uppercase; letter-spacing: 0.16em; font-size: 11px; color: rgba(239, 245, 255, 0.72); font-weight: 700; }
		.hero { display: flex; justify-content: space-between; align-items: flex-start; gap: 16px; margin-bottom: 16px; padding: 24px; border-radius: 18px; background: linear-gradient(135deg, rgba(19, 52, 95, 0.96), rgba(34, 92, 159, 0.92)); color: #eff5ff; box-shadow: 0 20px 34px rgba(16, 34, 59, 0.2); }
		.hero h1 { margin: 0 0 8px 0; font-family: Georgia, "Times New Roman", serif; letter-spacing: -0.02em; color: #fff; }
		.hero .subtle { color: rgba(239, 245, 255, 0.82); max-width: 820px; }
		.hero-actions { display: flex; gap: 10px; flex-wrap: wrap; justify-content: flex-end; }
		.button-link, .ghost-link { display: inline-flex; align-items: center; justify-content: center; gap: 8px; padding: 10px 14px; border-radius: 10px; text-decoration: none; font-weight: 700; }
		.button-link { background: #fff; color: #14335a; }
		.ghost-link { color: #eff5ff; border: 1px solid rgba(255, 255, 255, 0.2); background: rgba(255, 255, 255, 0.08); }
		.table-wrap { overflow-x: auto; margin-top: 12px; border-radius: 10px; }
		table { border-collapse: collapse; width: 100%; }
		td, th { border: 1px solid #c8d7e4; padding: 8px; text-align: left; vertical-align: top; }
		th { background: #12345c; color: #fff; }
		.section { background: #fff; border-radius: 12px; padding: 16px; box-shadow: 0 8px 20px rgba(8, 30, 54, 0.08); }
		a { color: #1a4f84; }
		.row-result { font-family: monospace; white-space: pre-wrap; }
		button { cursor: pointer; border: 0; background: #1b4b84; color: #fff; border-radius: 6px; padding: 6px 10px; }
		button:disabled { opacity: 0.5; cursor: not-allowed; }
		pre { background: #f8fcff; border: 1px solid #cfdce8; padding: 6px; }
		.ok { color: #146c2e; }
		.bad { color: #7f1d1d; }
		@media (max-width: 1024px) {
			.admin-shell { grid-template-columns: 1fr; }
			.sidebar { position: static; height: auto; }
			.sidebar nav { flex-direction: row; flex-wrap: wrap; }
			.sidebar nav a { padding: 8px 12px; }
			.hero { flex-direction: column; }
			.hero-actions { justify-content: flex-start; }
		}
	</style>
	<script>
		async function replayFromButton(buttonRef) {
			const { reference, provider, status, idempotencyKey } = buttonRef.dataset;
			if (!reference || !provider || !status || !idempotencyKey) {
				alert("reference, provider, status, dan idempotency_key wajib ada.");
				return;
			}
			buttonRef.disabled = true;
			try {
				const res = await fetch("/ui/callbacks/replay", {
					method: "POST",
					headers: { "Content-Type": "application/json" },
					body: JSON.stringify({
						reference,
						provider,
						status,
						idempotency_key: idempotencyKey,
					}),
				});
				const text = await res.text();
				let payload = text;
				try {
					payload = JSON.parse(text);
					payload = JSON.stringify(payload);
				} catch (_e) {}
				if (!res.ok) {
					alert("Replay gagal: " + payload);
					buttonRef.disabled = false;
					return;
				}
				alert("Replay sukses: " + payload);
				location.reload();
			} catch (err) {
				alert("Replay gagal: " + err);
				buttonRef.disabled = false;
			}
		}
	</script>
</head>
<body>
	<div class="admin-shell">
		<aside class="sidebar">
			<h2>Host RuteBayar</h2>
			<p class="muted">Dashboard Admin</p>
			<nav>
				<a href="/ui">🏠 Dashboard</a>
				<a href="/ui/callbacks" class="active">🔁 Callback Monitor</a>
				<a href="/ui#hosts">📋 Hosts</a>
				<a href="/ui#products">🧩 Products</a>
				<a href="/ui#orders">🧾 Orders</a>
				<a href="/ui/logout">🚪 Logout</a>
			</nav>
			<p class="muted">Monitor untuk delivery callback dan replay event.</p>
		</aside>
		<main class="content">
			<div class="hero">
				<div>
					<p class="eyebrow">Operations</p>
					<h1>Callback delivery monitor</h1>
					<p class="subtle">Pantau callback, lihat status delivery, dan replay event untuk investigasi. Gunakan panel ini untuk debugging alur webhook secara cepat.</p>
				</div>
				<div class="hero-actions">
					<a class="button-link" href="/ui">Kembali ke dashboard</a>
					<a class="ghost-link" href="#deliveries">Loncat ke daftar</a>
				</div>
			</div>
			<div class="section">
				<p class="subtle">Jumlah delivery tersimpan: <strong>{{len .Deliveries}}</strong></p>
				<div id="deliveries" class="table-wrap">
					<table>
						<tr><th>At</th><th>Reference</th><th>Provider</th><th>Status</th><th>Result</th><th>Idempotency</th><th>Attempts</th><th>Error</th><th>Action</th></tr>
						{{range .Deliveries}}
						<tr>
							<td>{{.At}}</td>
							<td>{{.Reference}}</td>
							<td>{{.Provider}}</td>
							<td>{{.Status}}</td>
							<td class="{{if eq .Result "failed"}}bad{{else}}ok{{end}}">{{.Result}}</td>
							<td>{{.IdempotencyKey}}</td>
							<td>{{.Attempts}}</td>
							<td class="row-result">{{.Error}}</td>
							<td>
								<button
									type="button"
									data-reference="{{.Reference}}"
									data-provider="{{.Provider}}"
									data-status="{{.Status}}"
									data-idempotency-key="{{.IdempotencyKey}}"
									onclick="replayFromButton(this)"
									{{if or (eq .Reference "") (eq .Provider "") (eq .Status "") (eq .IdempotencyKey "")}}disabled{{end}}
								>Replay</button>
							</td>
						</tr>
						{{else}}
						<tr><td colspan="9">Belum ada callback masuk.</td></tr>
						{{end}}
					</table>
				</div>
			</div>
		</main>
	</div>
</body>
</html>`
