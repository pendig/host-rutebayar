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
	if next == "" || strings.HasPrefix(next, "//") || !strings.HasPrefix(next, "/") {
		return "/ui"
	}
	u, err := url.Parse(next)
	if err != nil || u.Host != "" || u.Scheme != "" {
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
		Secure:   true,
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
		Secure:   true,
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
	mux.HandleFunc("/delete/host", requireAdminSession(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		var req struct {
			ID string `json:"id"`
		}
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			http.Error(w, "invalid body", http.StatusBadRequest)
			return
		}
		if err := orchestrator.DeleteHost(req.ID); err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]string{"message": "host deleted"})
	}))
	mux.HandleFunc("/delete/product", requireAdminSession(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		var req struct {
			ID string `json:"id"`
		}
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			http.Error(w, "invalid body", http.StatusBadRequest)
			return
		}
		if err := orchestrator.DeleteProduct(req.ID); err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]string{"message": "product deleted"})
	}))
	mux.HandleFunc("/delete/provider-account", requireAdminSession(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		var req struct {
			HostID   string `json:"host_id"`
			Provider string `json:"provider"`
			Env      string `json:"env"`
		}
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			http.Error(w, "invalid body", http.StatusBadRequest)
			return
		}
		if err := orchestrator.DeleteProviderAccount(req.HostID, req.Provider, req.Env); err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]string{"message": "provider account deleted"})
	}))
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
	status, attempts, err := orchestrator.ReconcileWebhookWithRetryWithAttempts(r.Context(), req.Reference, req.Provider, req.Status, req.IdempotencyKey)
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
	r.Body = http.MaxBytesReader(w, r.Body, 1<<20) // Limit to 1MB
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
		http.Error(w, err.Error(), http.StatusUnauthorized)
		return
	}
	status, attempts, err := orchestrator.ReconcileWebhookWithRetryWithAttempts(r.Context(), payload.Reference, provider, payload.Status, payload.IdempotencyKey)
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
	errStr := strings.ToLower(err.Error())
	return strings.Contains(errStr, "not found") || strings.Contains(errStr, "no rows")
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

	tmpl, err := template.New("ui").Funcs(template.FuncMap{
		"json": func(v interface{}) string {
			if v == nil {
				return ""
			}
			b, _ := json.Marshal(v)
			return string(b)
		},
	}).Parse(dashboardHTML)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	err = tmpl.Execute(w, map[string]interface{}{
		"Hosts":    hosts,
		"Products": products,
		"Orders":   orders,
	})
	if err != nil {
		fmt.Printf("Template execution error: %v\n", err)
		http.Error(w, err.Error(), http.StatusInternalServerError)
	}
}

const uiLoginHTML = `<!doctype html>
<html>
<head>
	<meta charset="utf-8"/>
	<title>Login Admin - Host RuteBayar</title>
	<link rel="preconnect" href="https://fonts.googleapis.com">
	<link rel="preconnect" href="https://fonts.gstatic.com" crossorigin>
	<link href="https://fonts.googleapis.com/css2?family=Plus+Jakarta+Sans:wght@400;500;600;700&display=swap" rel="stylesheet">
	<style>
		:root {
			--primary: hsl(220, 85%, 32%);
			--primary-hover: hsl(220, 85%, 26%);
			--bg: radial-gradient(circle at 12% 0%, hsl(220, 60%, 97%) 0%, hsl(220, 60%, 95%) 36%, hsl(220, 60%, 98%) 100%);
			--card-bg: #ffffff;
			--text: hsl(210, 40%, 15%);
			--text-muted: hsl(210, 15%, 45%);
			--border: hsl(210, 30%, 88%);
			--border-focus: hsl(220, 85%, 60%);
			--error-bg: hsl(0, 100%, 97%);
			--error-text: hsl(0, 75%, 35%);
			--error-border: hsl(0, 80%, 92%);
		}
		body {
			margin: 0;
			font-family: "Plus Jakarta Sans", -apple-system, BlinkMacSystemFont, "Segoe UI", Roboto, sans-serif;
			color: var(--text);
			background: var(--bg);
			min-height: 100vh;
			display: grid;
			place-items: center;
			padding: 24px;
			box-sizing: border-box;
		}
		.login-shell {
			width: min(420px, 100%);
			background: var(--card-bg);
			border-radius: 16px;
			padding: 40px 32px;
			box-shadow: 0 10px 25px -5px rgba(15, 30, 60, 0.05), 0 20px 48px -10px rgba(15, 30, 60, 0.12);
			border: 1px solid var(--border);
		}
		h1 {
			margin: 0 0 8px 0;
			font-size: 24px;
			font-weight: 700;
			color: var(--primary);
			letter-spacing: -0.02em;
		}
		p {
			margin: 0 0 24px 0;
			color: var(--text-muted);
			font-size: 14px;
			line-height: 1.5;
		}
		label {
			display: block;
			margin-bottom: 20px;
			font-size: 14px;
			font-weight: 500;
			color: var(--text);
		}
		input {
			width: 100%;
			box-sizing: border-box;
			padding: 12px 14px;
			margin-top: 8px;
			border-radius: 10px;
			border: 1px solid var(--border);
			font-family: inherit;
			font-size: 14px;
			color: var(--text);
			background-color: hsl(210, 40%, 99%);
			transition: all 0.2s ease;
		}
		input:focus {
			outline: none;
			border-color: var(--border-focus);
			background-color: #ffffff;
			box-shadow: 0 0 0 3px rgba(37, 99, 235, 0.12);
		}
		button {
			width: 100%;
			padding: 12px;
			border: 0;
			border-radius: 10px;
			color: #fff;
			background: var(--primary);
			font-weight: 600;
			font-size: 14px;
			cursor: pointer;
			font-family: inherit;
			transition: all 0.2s ease;
		}
		button:hover {
			background: var(--primary-hover);
			transform: translateY(-1px);
			box-shadow: 0 4px 12px rgba(30, 58, 138, 0.2);
		}
		button:active {
			transform: translateY(0);
		}
		.error {
			color: var(--error-text);
			background: var(--error-bg);
			border: 1px solid var(--error-border);
			border-radius: 10px;
			padding: 12px 14px;
			margin-bottom: 20px;
			font-size: 14px;
			font-weight: 500;
		}
		small {
			color: var(--text-muted);
			display: block;
			margin-top: 24px;
			font-size: 12px;
			text-align: center;
			line-height: 1.5;
		}
		strong {
			color: var(--text);
		}
	</style>
</head>
<body>
	<div class="login-shell">
		<h1>Login Admin</h1>
		<p>Masukkan password untuk membuka dashboard operasi self-hosted.</p>
		{{if .Message}}<div class="error">Login gagal, silakan coba lagi.</div>{{end}}
		<form method="post" action="/ui/login">
			<label>Password
				<input type="password" name="password" autocomplete="current-password" required />
			</label>
			<input type="hidden" name="next" value="{{.Next}}" />
			<button type="submit">Masuk</button>
		</form>
		<small>Default password dapat dikonfigurasi melalui env <strong>HOST_RUTEBAYAR_ADMIN_PASSWORD</strong>.</small>
	</div>
</body>
</html>`

const dashboardHTML = `<!doctype html>
<html>
<head>
	<meta charset="utf-8"/>
	<title>host-rutebayar self-hosted</title>
	<link rel="preconnect" href="https://fonts.googleapis.com">
	<link rel="preconnect" href="https://fonts.gstatic.com" crossorigin>
	<link href="https://fonts.googleapis.com/css2?family=Plus+Jakarta+Sans:wght@400;500;600;700&display=swap" rel="stylesheet">
	<style>
		:root {
			--primary: hsl(220, 85%, 32%);
			--primary-hover: hsl(220, 85%, 26%);
			--primary-light: hsl(220, 85%, 97%);
			--bg: hsl(210, 40%, 98%);
			--card-bg: #ffffff;
			--text: hsl(210, 40%, 15%);
			--text-muted: hsl(210, 15%, 45%);
			--border: hsl(210, 30%, 88%);
			--border-focus: hsl(220, 85%, 60%);
			--shadow-sm: 0 1px 2px 0 rgba(0, 0, 0, 0.05);
			--shadow: 0 4px 6px -1px rgba(0, 0, 0, 0.05), 0 2px 4px -1px rgba(0, 0, 0, 0.03);
			--shadow-lg: 0 10px 25px -5px rgba(15, 30, 60, 0.05), 0 20px 48px -10px rgba(15, 30, 60, 0.08);
			--radius: 12px;
			--radius-sm: 8px;
		}
		body { font-family: "Plus Jakarta Sans", -apple-system, BlinkMacSystemFont, "Segoe UI", Roboto, sans-serif; margin: 0; color: var(--text); background: var(--bg); }
		a { color: var(--primary); text-decoration: none; font-weight: 500; transition: color 0.2s ease; }
		a:hover { color: var(--primary-hover); text-decoration: underline; }
		h1, h2, h3 { margin-top: 0; font-weight: 700; color: var(--text); letter-spacing: -0.025em; }
		.section { background: var(--card-bg); border-radius: var(--radius); padding: 24px; margin-bottom: 24px; box-shadow: var(--shadow); border: 1px solid var(--border); transition: transform 0.2s ease, box-shadow 0.2s ease; }
		.section:hover { box-shadow: var(--shadow-lg); }
		table { border-collapse: collapse; width: 100%; margin-top: 12px; }
		th, td { border-bottom: 1px solid var(--border); padding: 12px 16px; text-align: left; vertical-align: middle; font-size: 14px; }
		tr:hover td { background-color: hsl(210, 40%, 99%); }
		th { background: hsl(210, 40%, 96%); color: var(--text); font-weight: 600; border-top: 1px solid var(--border); }
		.admin-shell { display: grid; grid-template-columns: 270px minmax(0, 1fr); min-height: 100vh; }
		.sidebar { position: sticky; top: 0; height: 100vh; overflow-y: auto; padding: 32px 24px; background: linear-gradient(160deg, hsl(220, 85%, 20%), hsl(220, 85%, 15%)); color: #ecf3ff; display: flex; flex-direction: column; justify-content: space-between; }
		.sidebar-top { display: flex; flex-direction: column; }
		.sidebar h2 { margin: 0 0 8px 0; font-size: 20px; font-weight: 700; color: #ffffff; letter-spacing: -0.02em; }
		.sidebar nav { display: flex; flex-direction: column; gap: 8px; margin-top: 24px; }
		.sidebar a { color: hsl(220, 60%, 85%); text-decoration: none; font-size: 14px; font-weight: 500; border-radius: var(--radius-sm); padding: 12px 14px; transition: all 0.2s ease; }
		.sidebar a:hover { color: #ffffff; background: rgba(255, 255, 255, 0.08); }
		.sidebar .active { background: rgba(255, 255, 255, 0.15); color: #ffffff; font-weight: 600; }
		.sidebar .muted { margin-top: 16px; opacity: 0.7; font-size: 12px; line-height: 1.5; }
		.content { padding: 40px 32px; overflow-x: auto; max-width: 1400px; margin: 0 auto; width: 100%; box-sizing: border-box; }
		.top-actions { display: flex; gap: 12px; align-items: center; justify-content: space-between; flex-wrap: wrap; }
		.subtle { color: var(--text-muted); font-size: 14px; line-height: 1.6; }
		.eyebrow { margin: 0 0 6px 0; text-transform: uppercase; letter-spacing: 0.12em; font-size: 11px; color: var(--text-muted); font-weight: 700; }
		.hero { display: flex; gap: 24px; justify-content: space-between; align-items: center; padding: 32px; margin-bottom: 32px; border-radius: var(--radius); background: linear-gradient(135deg, hsl(220, 85%, 28%), hsl(220, 85%, 18%)); color: #eff5ff; box-shadow: var(--shadow-lg); }
		.hero h1 { margin: 0 0 8px 0; color: #fff; font-size: 30px; letter-spacing: -0.02em; }
		.hero .subtle { color: rgba(239, 245, 255, 0.8); max-width: 760px; font-size: 15px; }
		.hero-actions { display: flex; gap: 12px; flex-wrap: wrap; }
		.button-link, .ghost-link { display: inline-flex; align-items: center; justify-content: center; gap: 8px; padding: 10px 18px; border-radius: var(--radius-sm); text-decoration: none; font-weight: 600; font-size: 14px; transition: all 0.2s ease; cursor: pointer; border: 0; }
		.button-link { background: #fff; color: hsl(220, 85%, 25%); box-shadow: var(--shadow-sm); }
		.button-link:hover { transform: translateY(-1px); box-shadow: var(--shadow); background: hsl(210, 40%, 98%); }
		.ghost-link { color: #eff5ff; border: 1px solid rgba(255, 255, 255, 0.2); background: rgba(255, 255, 255, 0.06); }
		.ghost-link:hover { background: rgba(255, 255, 255, 0.12); border-color: rgba(255, 255, 255, 0.3); }
		.stats { display: grid; grid-template-columns: repeat(auto-fit, minmax(200px, 1fr)); gap: 16px; margin: 0 0 28px 0; }
		.stat { padding: 20px; border-radius: var(--radius); background: var(--card-bg); border: 1px solid var(--border); box-shadow: var(--shadow); }
		.stat strong { display: block; font-size: 32px; font-weight: 700; line-height: 1.1; margin-top: 10px; color: var(--primary); }
		.stat span { color: var(--text-muted); font-size: 13px; font-weight: 500; }
		.table-wrap { overflow-x: auto; border: 1px solid var(--border); border-radius: var(--radius-sm); background: var(--card-bg); }
		.pill { display: inline-flex; align-items: center; border-radius: 999px; padding: 4px 12px; background: var(--primary-light); color: var(--primary); font-size: 11px; font-weight: 700; text-transform: uppercase; letter-spacing: 0.05em; }
		.grid { display: grid; grid-template-columns: repeat(auto-fit, minmax(320px, 1fr)); gap: 24px; margin-bottom: 24px; }
		label { display: block; margin-bottom: 16px; font-size: 14px; font-weight: 500; color: var(--text); }
		input, textarea, select { width: 100%; box-sizing: border-box; padding: 10px 12px; margin-top: 6px; border-radius: var(--radius-sm); border: 1px solid var(--border); font-family: inherit; font-size: 14px; background: hsl(210, 40%, 99%); color: var(--text); transition: all 0.2s ease; }
		input:focus, textarea:focus, select:focus { outline: none; border-color: var(--border-focus); background: #ffffff; box-shadow: 0 0 0 3px rgba(37, 99, 235, 0.12); }
		textarea { min-height: 80px; font-family: monospace; resize: vertical; }
		button { background: var(--primary); color: #fff; font-weight: 600; cursor: pointer; border: 0; padding: 11px 16px; border-radius: var(--radius-sm); transition: all 0.2s ease; font-family: inherit; font-size: 14px; }
		button:hover { background: var(--primary-hover); transform: translateY(-1px); box-shadow: 0 4px 10px rgba(30, 58, 138, 0.15); }
		button:active { transform: translateY(0); }
		.small-btn { width: auto; padding: 6px 12px; font-size: 13px; font-weight: 600; border-radius: 6px; }
		.msg { margin-top: 14px; padding: 12px 16px; border-radius: var(--radius-sm); font-size: 14px; font-weight: 500; display: none; animation: fadeIn 0.3s ease; }
		.msg.ok { background: hsl(120, 70%, 97%); border: 1px solid hsl(120, 60%, 90%); color: hsl(120, 80%, 25%); }
		.msg.err { background: hsl(0, 100%, 97%); border: 1px solid hsl(0, 80%, 92%); color: hsl(0, 75%, 35%); }
		.badge { display: inline-flex; align-items: center; padding: 4px 10px; font-size: 11px; font-weight: 700; border-radius: 9999px; text-transform: uppercase; letter-spacing: 0.05em; }
		.badge-success { background: hsl(120, 70%, 95%); color: hsl(120, 80%, 25%); }
		.badge-warning { background: hsl(40, 90%, 95%); color: hsl(40, 90%, 30%); }
		.badge-danger { background: hsl(0, 100%, 95%); color: hsl(0, 80%, 35%); }
		.badge-info { background: hsl(190, 90%, 95%); color: hsl(190, 90%, 30%); }
		.badge-secondary { background: hsl(210, 30%, 94%); color: hsl(210, 30%, 30%); }
		pre { font-family: ui-monospace, SFMono-Regular, Menlo, Monaco, Consolas, monospace; font-size: 12px; background: hsl(210, 40%, 99%); padding: 12px; border: 1px solid var(--border); border-radius: var(--radius-sm); overflow-x: auto; margin: 0; color: hsl(210, 30%, 25%); text-align: left; }
		pre:empty { display: none; }
		
		/* Wizard styling */
		.wizard-section { background: var(--card-bg); border-radius: var(--radius); border: 1px solid var(--border); padding: 28px; margin-bottom: 24px; box-shadow: var(--shadow); }
		.tab-headers { display: flex; gap: 8px; border-bottom: 2px solid var(--border); padding-bottom: 12px; margin-bottom: 24px; overflow-x: auto; }
		.tab-btn { background: none; color: var(--text-muted); font-weight: 600; padding: 10px 16px; border-radius: var(--radius-sm); border: 0; transition: all 0.2s ease; cursor: pointer; white-space: nowrap; width: auto; font-size: 14px; }
		.tab-btn:hover { background: var(--bg); color: var(--text); }
		.tab-btn.active { background: var(--primary-light); color: var(--primary); }
		.tab-pane { display: none; }
		.tab-pane.active { display: block; }
		
		/* Password wrapper */
		.pwd-wrapper { position: relative; display: flex; align-items: center; }
		.pwd-wrapper input { padding-right: 44px; margin-top: 0; }
		.pwd-toggle { position: absolute; right: 14px; top: 50%; transform: translateY(-50%); cursor: pointer; user-select: none; font-size: 16px; opacity: 0.6; transition: opacity 0.2s ease; }
		.pwd-toggle:hover { opacity: 1; }

		/* SPA view panes */
		.view-pane { display: none; }
		.view-pane.active { display: block; animation: fadeIn 0.3s ease; }

		@keyframes fadeIn {
			from { opacity: 0; transform: translateY(-5px); }
			to { opacity: 1; transform: translateY(0); }
		}
		@media (max-width: 1024px) {
			.admin-shell { grid-template-columns: 1fr; }
			.sidebar { position: static; height: auto; padding: 24px; }
			.sidebar nav { flex-direction: row; flex-wrap: wrap; margin-top: 16px; gap: 8px; }
			.sidebar nav a { padding: 8px 12px; }
			.hero { flex-direction: column; align-items: flex-start; text-align: left; }
			.hero-actions { width: 100%; justify-content: flex-start; margin-top: 12px; }
		}
	</style>
	<script>
		function showToast(message, type = "success") {
			let container = document.getElementById("toast-container");
			if (!container) {
				container = document.createElement("div");
				container.id = "toast-container";
				container.style.position = "fixed";
				container.style.top = "24px";
				container.style.right = "24px";
				container.style.zIndex = "9999";
				container.style.display = "flex";
				container.style.flexDirection = "column";
				container.style.gap = "8px";
				document.body.appendChild(container);
			}
			const toast = document.createElement("div");
			toast.style.background = type === "success" ? "hsl(120, 70%, 25%)" : "hsl(0, 80%, 35%)";
			toast.style.color = "#fff";
			toast.style.padding = "12px 20px";
			toast.style.borderRadius = "8px";
			toast.style.boxShadow = "0 4px 12px rgba(0,0,0,0.15)";
			toast.style.fontFamily = "inherit";
			toast.style.fontSize = "14px";
			toast.style.fontWeight = "600";
			toast.style.minWidth = "280px";
			toast.style.transition = "all 0.3s ease";
			toast.style.opacity = "0";
			toast.style.transform = "translateY(-20px)";
			toast.textContent = message;

			container.appendChild(toast);

			setTimeout(() => {
				toast.style.opacity = "1";
				toast.style.transform = "translateY(0)";
			}, 10);

			setTimeout(() => {
				toast.style.opacity = "0";
				toast.style.transform = "translateY(-20px)";
				setTimeout(() => toast.remove(), 300);
			}, 3500);
		}

		function togglePassword(id) {
			const input = document.getElementById(id);
			if (input.type === "password") {
				input.type = "text";
			} else {
				input.type = "password";
			}
		}

		function showView(viewId, event) {
			if (event) event.preventDefault();
			document.querySelectorAll(".view-pane").forEach(pane => pane.classList.remove("active"));
			document.getElementById(viewId).classList.add("active");
			
			document.querySelectorAll(".sidebar nav a").forEach(link => link.classList.remove("active"));
			const activeLink = Array.from(document.querySelectorAll(".sidebar nav a")).find(link => {
				const attr = link.getAttribute("onclick") || "";
				return attr.includes(viewId);
			});
			if (activeLink) activeLink.classList.add("active");
			
			const hash = viewId.replace("view-", "");
			history.pushState(null, null, "#" + hash);
		}

		function switchTab(tabId) {
			document.querySelectorAll(".tab-pane").forEach(pane => pane.classList.remove("active"));
			document.querySelectorAll(".tab-btn").forEach(btn => btn.classList.remove("active"));
			
			document.getElementById(tabId).classList.add("active");
			const btns = Array.from(document.querySelectorAll(".tab-btn"));
			const activeBtn = btns.find(btn => btn.getAttribute("onclick").includes(tabId));
			if (activeBtn) activeBtn.classList.add("active");
		}

		function editHost(id, name, notificationKey, hostSecret, webhookSecret, callbackUrls, callbackAllowlist) {
			showView("view-dashboard");
			document.getElementById("host-id").value = id;
			document.getElementById("host-id").readOnly = true;
			document.getElementById("host-name").value = name;
			document.getElementById("host-notification-key").value = notificationKey;
			document.getElementById("host-secret").value = hostSecret;
			document.getElementById("host-webhook-secret").value = webhookSecret;
			document.getElementById("host-callback-urls").value = callbackUrls;
			document.getElementById("host-callback-allowlist").value = callbackAllowlist;
			
			switchTab("tab-host");
			document.querySelector(".wizard-section").scrollIntoView({ behavior: "smooth" });
			showToast("Form Host diisi. Edit dan simpan untuk memperbarui.", "success");
		}

		function editProduct(id, hostId, name, sku, price, meta, override, isActive) {
			showView("view-dashboard");
			document.getElementById("product-id").value = id;
			document.getElementById("product-id").readOnly = true;
			document.getElementById("product-host-id").value = hostId;
			document.getElementById("product-name").value = name;
			document.getElementById("product-sku").value = sku;
			document.getElementById("product-price").value = price;
			document.getElementById("product-meta").value = meta || "{}";
			document.getElementById("product-fee-override").value = override || "";
			document.getElementById("product-active").checked = isActive;
			
			switchTab("tab-product");
			document.querySelector(".wizard-section").scrollIntoView({ behavior: "smooth" });
			showToast("Form Produk diisi. Edit dan simpan untuk memperbarui.", "success");
		}

		function triggerEditHost(btn) {
			const id = btn.getAttribute("data-id");
			const name = btn.getAttribute("data-name");
			const notificationKey = btn.getAttribute("data-notification-key");
			const hostSecret = btn.getAttribute("data-host-secret");
			const webhookSecret = btn.getAttribute("data-webhook-secret");
			const callbackUrls = btn.getAttribute("data-endpoints");
			const callbackAllowlist = btn.getAttribute("data-allowlist");
			document.getElementById("host-id").dataset.originalSecret = hostSecret;
			editHost(id, name, notificationKey, hostSecret, webhookSecret, callbackUrls, callbackAllowlist);
		}

		function triggerEditProduct(btn) {
			const id = btn.getAttribute("data-id");
			const hostId = btn.getAttribute("data-host-id");
			const name = btn.getAttribute("data-name");
			const sku = btn.getAttribute("data-sku");
			const price = btn.getAttribute("data-price");
			const meta = btn.getAttribute("data-meta");
			const override = btn.getAttribute("data-override");
			const isActive = btn.getAttribute("data-active") === "true";
			editProduct(id, hostId, name, sku, price, meta, override, isActive);
		}

		async function deleteHost(id) {
			if (!confirm("Apakah Anda yakin ingin menghapus host '" + id + "'? Semua produk, policy, dan provider account terkait juga akan terhapus.")) return;
			try {
				await postJSON("/delete/host", { id });
				showToast("Host berhasil dihapus!", "success");
				setTimeout(() => location.reload(), 1000);
			} catch (err) {
				showToast("Gagal menghapus host: " + err.message, "error");
			}
		}

		async function deleteProduct(id) {
			if (!confirm("Apakah Anda yakin ingin menghapus produk '" + id + "'?")) return;
			try {
				await postJSON("/delete/product", { id });
				showToast("Produk berhasil dihapus!", "success");
				setTimeout(() => location.reload(), 1000);
			} catch (err) {
				showToast("Gagal menghapus produk: " + err.message, "error");
			}
		}

		function splitCSV(value) {
			return value.split(",").map((s) => s.trim()).filter((s) => s.length > 0);
		}
		function showMessage(message, isOk) {
			showToast(message, isOk ? "success" : "error");
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
				const headers = {};
				const originalSecret = document.getElementById("host-id").dataset.originalSecret;
				if (originalSecret) {
					headers["X-Host-Secret"] = originalSecret;
				}
				await postJSON("/register/host", payload, headers);
				showToast("Host berhasil disimpan. Memuat ulang...", true);
				setTimeout(() => location.reload(), 600);
			} catch (err) {
				showToast("Gagal simpan host: " + err.message, false);
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
				showToast("Produk berhasil disimpan. Memuat ulang...", true);
				setTimeout(() => location.reload(), 600);
			} catch (err) {
				showToast("Gagal simpan produk: " + err.message, false);
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
				showToast("Provider account berhasil disimpan. Memuat ulang...", true);
				setTimeout(() => location.reload(), 600);
			} catch (err) {
				showToast("Gagal simpan provider account: " + err.message, false);
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
				showToast("Host policy berhasil disimpan. Memuat ulang...", true);
				setTimeout(() => location.reload(), 600);
			} catch (err) {
				showToast("Gagal simpan host policy: " + err.message, false);
			}
		}
		async function seedDemo(event) {
			event.preventDefault();
			try {
				const response = await postJSON("/admin/demo-seed", {});
				document.getElementById("seed-output").textContent = JSON.stringify(response, null, 2);
				showToast("Data demo berhasil dibuat. Memuat ulang...", true);
				setTimeout(() => location.reload(), 600);
			} catch (err) {
				showToast("Seed gagal: " + err.message, false);
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
				showToast("Test payment berhasil dibuat.", true);
			} catch (err) {
				showToast("Test payment gagal: " + err.message, false);
			}
		}
		window.addEventListener("DOMContentLoaded", () => {
			const hostSelector = document.getElementById("test-host-id");
			if (hostSelector) {
				hostSelector.addEventListener("change", syncProductOptions);
				syncProductOptions();
			}

			// Format pre elements with JSON
			document.querySelectorAll("pre").forEach(pre => {
				try {
					const obj = JSON.parse(pre.textContent);
					pre.textContent = JSON.stringify(obj, null, 2);
				} catch(e) {}
			});

			// Map and render statuses with premium badges
			const cells = document.querySelectorAll("td");
			cells.forEach(td => {
				const text = td.textContent.trim().toUpperCase();
				if (text === "PAID" || text === "SUCCESS" || text === "OK") {
					td.innerHTML = '<span class="badge badge-success">' + text + '</span>';
				} else if (text === "PENDING" || text === "SUBMITTED") {
					td.innerHTML = '<span class="badge badge-warning">' + text + '</span>';
				} else if (text === "FAILED" || text === "BAD" || text === "EXPIRED") {
					td.innerHTML = '<span class="badge badge-danger">' + text + '</span>';
				} else if (text === "SANDBOX") {
					td.innerHTML = '<span class="badge badge-info">' + text + '</span>';
				} else if (text === "PRODUCTION" || text === "PROD") {
					td.innerHTML = '<span class="badge badge-secondary">' + text + '</span>';
				} else if (text === "TRUE") {
					td.innerHTML = '<span class="badge badge-success">ACTIVE</span>';
				} else if (text === "FALSE") {
					td.innerHTML = '<span class="badge badge-danger">INACTIVE</span>';
				}
			});
			// Handle URL hash on load
			const hash = window.location.hash;
			if (hash === "#hosts") {
				showView("view-hosts");
			} else if (hash === "#products") {
				showView("view-products");
			} else if (hash === "#orders") {
				showView("view-orders");
			} else {
				showView("view-dashboard");
			}
		});
	</script>
</head>
<body>
	<div class="admin-shell">
		<aside class="sidebar">
			<div class="sidebar-top">
				<h2>Host RuteBayar</h2>
				<p class="muted" style="margin-top:0;">Dashboard Admin</p>
				<nav>
					<a href="#" onclick="showView('view-dashboard', event)" class="active">🏠 Dashboard</a>
					<a href="/ui/callbacks">🔁 Callback Monitor</a>
					<a href="#" onclick="showView('view-hosts', event)">📋 Hosts</a>
					<a href="#" onclick="showView('view-products', event)">🧩 Products</a>
					<a href="#" onclick="showView('view-orders', event)">🧾 Orders</a>
				</nav>
			</div>
			<div>
				<a href="/ui/logout" onclick="return confirm('Apakah Anda yakin ingin logout?');" style="display: inline-flex; align-items: center; gap: 8px; padding: 10px 14px; border-radius: var(--radius-sm); width: 100%; box-sizing: border-box; color: hsl(0, 100%, 80%); transition: all 0.2s ease;">🚪 Logout</a>
				<p class="muted" style="margin-top: 12px; margin-bottom: 0; font-size: 12px; opacity: 0.7;">Akses jalur ini untuk operasional lokal.</p>
			</div>
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
					<a class="ghost-link" href="#" onclick="showView('view-orders', event)">Loncat ke orders</a>
				</div>
			</div>
			<!-- DASHBOARD VIEW -->
			<div id="view-dashboard" class="view-pane active">
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
				<div class="wizard-section">
					<div style="margin-bottom: 20px;">
						<h2 style="margin:0 0 6px 0;">⚙️ Setup Wizard Onboarding</h2>
						<p class="subtle" style="margin:0;">Ikuti langkah-langkah di bawah ini untuk onboarding Host baru atau edit konfigurasi yang sudah ada.</p>
					</div>
					
					<div class="tab-headers">
						<button class="tab-btn active" type="button" onclick="switchTab('tab-host')">📋 1. Host Profile</button>
						<button class="tab-btn" type="button" onclick="switchTab('tab-account')">🔌 2. Provider Account</button>
						<button class="tab-btn" type="button" onclick="switchTab('tab-policy')">💸 3. Default Policy</button>
						<button class="tab-btn" type="button" onclick="switchTab('tab-product')">📦 4. Product Catalog</button>
						<button class="tab-btn" type="button" onclick="switchTab('tab-test')">🧪 5. Smoke Test</button>
					</div>

					<div class="tab-contents">
						<!-- STEP 1: HOST PROFILE -->
						<div id="tab-host" class="tab-pane active">
							<form onsubmit="submitHost(event)">
								<div class="grid">
									<label>Host ID (unik, tanpa spasi)
										<input id="host-id" placeholder="contoh: merchant_sukses" required />
									</label>
									<label>Nama Host / Toko
										<input id="host-name" placeholder="contoh: Toko Maju Jaya" required />
									</label>
									<label>Notification Key
										<input id="host-notification-key" placeholder="contoh: nkey_prod_abcdef..." required />
									</label>
								</div>
								<div class="grid">
									<label>Host Secret
										<div class="pwd-wrapper">
											<input type="password" id="host-secret" placeholder="Secret Key untuk API host" required />
											<span class="pwd-toggle" onclick="togglePassword('host-secret')">👁️</span>
										</div>
									</label>
									<label>Webhook Secret
										<div class="pwd-wrapper">
											<input type="password" id="host-webhook-secret" placeholder="Secret Key untuk tanda tangan webhook" required />
											<span class="pwd-toggle" onclick="togglePassword('host-webhook-secret')">👁️</span>
										</div>
									</label>
								</div>
								<div class="grid">
									<label>Callback URLs (pisahkan dengan koma)
										<textarea id="host-callback-urls" placeholder="https://api.toko.com/webhook, https://api.toko.com/backup-webhook">https://example.com/callback</textarea>
									</label>
									<label>Callback Allowlist Domain (koma)
										<textarea id="host-callback-allowlist" placeholder="https://api.toko.com, https://toko.com">https://example.com</textarea>
									</label>
								</div>
								<button type="submit">Simpan Profile Host</button>
							</form>
						</div>

						<!-- STEP 2: PROVIDER ACCOUNT -->
						<div id="tab-account" class="tab-pane">
							<form onsubmit="submitProviderAccount(event)">
								<div class="grid">
									<label>Host
										<select id="provider-host-id" required>
											<option value="">Pilih Host Terdaftar</option>
											{{range .Hosts}}<option value="{{.ID}}">{{.ID}}</option>{{end}}
										</select>
									</label>
									<label>Host Secret (untuk verifikasi)
										<div class="pwd-wrapper">
											<input id="provider-host-secret" type="password" placeholder="Masukkan secret key host ini" required />
											<span class="pwd-toggle" onclick="togglePassword('provider-host-secret')">👁️</span>
										</div>
									</label>
								</div>
								<div class="grid">
									<label>Provider Gateway
										<select id="provider-name" required>
											<option value="midtrans">Midtrans</option>
											<option value="xendit">Xendit</option>
											<option value="doku">Doku</option>
											<option value="ipaymu">IPaymu</option>
										</select>
									</label>
									<label>Environment
										<select id="provider-env" required>
											<option value="sandbox">Sandbox (Testing)</option>
											<option value="production">Production (Real)</option>
										</select>
									</label>
								</div>
								<div class="grid">
									<label>Provider Credentials Hash/Key
										<div class="pwd-wrapper">
											<input id="provider-credentials" type="password" placeholder="Server Key / API Secret dari payment gateway" required />
											<span class="pwd-toggle" onclick="togglePassword('provider-credentials')">👁️</span>
										</div>
									</label>
									<label>Public Config JSON (opsional)
										<textarea id="provider-config" placeholder='{"merchant_id": "G12345678"}'></textarea>
									</label>
								</div>
								<button type="submit">Hubungkan Akun Gateway</button>
							</form>
						</div>

						<!-- STEP 3: DEFAULT POLICY -->
						<div id="tab-policy" class="tab-pane">
							<form onsubmit="submitHostPolicy(event)">
								<div class="grid">
									<label>Host
										<select id="policy-host-id" required>
											<option value="">Pilih Host Terdaftar</option>
											{{range .Hosts}}<option value="{{.ID}}">{{.ID}}</option>{{end}}
										</select>
									</label>
									<label>Host Secret (untuk verifikasi)
										<div class="pwd-wrapper">
											<input id="policy-host-secret" type="password" placeholder="Masukkan secret key host" required />
											<span class="pwd-toggle" onclick="togglePassword('policy-host-secret')">👁️</span>
										</div>
									</label>
								</div>
								<div class="grid">
									<label>Tipe Komisi (Fee Type)
										<select id="policy-type">
											<option value="percent">Persentase (%)</option>
											<option value="fixed">Nominal Tetap (Rp)</option>
											<option value="free">Gratis (0 Fee)</option>
										</select>
									</label>
									<label>Nilai Komisi
										<input id="policy-value" type="number" step="0.1" value="2.0" placeholder="Contoh: 2.5 untuk persen, atau 5000 untuk tetap" required />
									</label>
								</div>
								<div class="grid">
									<label>Mata Uang
										<input id="policy-currency" value="IDR" placeholder="IDR" />
									</label>
									<label>Rounding Policy
										<select id="policy-rounding">
											<option value="nearest">Nearest (Pembulatan Terdekat)</option>
											<option value="ceil">Ceil (Pembulatan ke Atas)</option>
											<option value="floor">Floor (Pembulatan ke Bawah)</option>
										</select>
									</label>
								</div>
								<div class="grid">
									<label>Minimal Komisi (Nominal, opsional)
										<input id="policy-min-fee" type="number" placeholder="Contoh: 1000" />
									</label>
									<label>Maksimal Komisi (Nominal, opsional)
										<input id="policy-max-fee" type="number" placeholder="Contoh: 50000" />
									</label>
									<label>Policy Version
										<input id="policy-version" value="v1" placeholder="v1" />
									</label>
								</div>
								<button type="submit">Terapkan Policy Default</button>
							</form>
						</div>

						<!-- STEP 4: PRODUCT CATALOG -->
						<div id="tab-product" class="tab-pane">
							<form onsubmit="submitProduct(event)">
								<div class="grid">
									<label>Host Pemilik Produk
										<select id="product-host-id" required>
											<option value="">Pilih Host</option>
											{{range .Hosts}}<option value="{{.ID}}">{{.ID}}</option>{{end}}
										</select>
									</label>
									<label>Host Secret (untuk verifikasi)
										<div class="pwd-wrapper">
											<input id="product-host-secret" type="password" placeholder="Masukkan secret key host" required />
											<span class="pwd-toggle" onclick="togglePassword('product-host-secret')">👁️</span>
										</div>
									</label>
								</div>
								<div class="grid">
									<label>Product ID (unik)
										<input id="product-id" placeholder="contoh: kaos_oblong_01" required />
									</label>
									<label>Nama Produk
										<input id="product-name" placeholder="contoh: Kaos Oblong Polos Cotton" required />
									</label>
									<label>SKU Produk
										<input id="product-sku" placeholder="contoh: SKU-KAOS-COTTON-01" required />
									</label>
								</div>
								<div class="grid">
									<label>Harga Produk (Rupiah, nominal bulat)
										<input id="product-price" type="number" min="0" step="1" placeholder="contoh: 85000" required />
									</label>
									<label>Custom Meta JSON (opsional)
										<textarea id="product-meta" placeholder='{"kategori": "pakaian"}'></textarea>
									</label>
									<label>Policy Override JSON (opsional)
										<textarea id="product-fee-override" placeholder='{"type": "fixed", "value": 1000}'></textarea>
									</label>
								</div>
								<div style="margin-bottom: 20px;">
									<label style="display:flex; align-items:center; gap:8px; cursor:pointer;">
										<input id="product-active" type="checkbox" checked style="width:auto; margin:0;" />
										<span>Aktifkan produk ini agar langsung bisa dibeli</span>
									</label>
								</div>
								<button type="submit">Daftarkan Produk</button>
							</form>
						</div>

						<!-- STEP 5: SMOKE TEST -->
						<div id="tab-test" class="tab-pane">
							<p class="subtle" style="margin-top: 0; margin-bottom: 20px;">Simulasikan transaksi checkout dari sisi pembeli untuk memverifikasi alur fee policy & provider integration.</p>
							<form onsubmit="createTestPayment(event)">
								<div class="grid">
									<label>Pilih Host
										<select id="test-host-id" required>
											<option value="">Pilih Host</option>
											{{range .Hosts}}<option value="{{.ID}}">{{.ID}}</option>{{end}}
										</select>
									</label>
									<label>Pilih Produk
										<select id="test-product-id" required>
											<option value="">Pilih Produk</option>
											{{range .Products}}<option value="{{.ID}}" data-host="{{.HostID}}">{{.ID}}</option>{{end}}
										</select>
									</label>
								</div>
								<div class="grid">
									<label>Environment Simulasi
										<select id="test-env" required>
											<option value="sandbox">Sandbox (Testing)</option>
											<option value="production">Production</option>
										</select>
									</label>
									<label>Buyer Reference ID (opsional)
										<input id="test-buyer-ref" placeholder="contoh: buyer_sholih_99" />
									</label>
								</div>
								<button type="submit">Buat Simulasikan Transaksi</button>
							</form>
							<div class="table-wrap" style="margin-top:20px;"><pre id="test-output"></pre></div>
						</div>
					</div>
				</div>
			</div>

			<!-- HOSTS VIEW -->
			<div id="view-hosts" class="view-pane">
				<div id="hosts" class="section">
					<h2>Hosts</h2>
					<div class="table-wrap">
						<table>
							<tr><th>ID</th><th>Nama</th><th>Callback URL</th><th>Allowlist</th><th>Aksi</th></tr>
							{{range .Hosts}}
							<tr>
								<td><a href="/ui/host/{{.ID}}">{{.ID}}</a></td>
								<td>{{.Name}}</td>
								<td><pre>{{range .CallbackURLs}}{{.}} {{end}}</pre></td>
								<td><pre>{{range .CallbackAllowlist}}{{.}} {{end}}</pre></td>
								<td>
									<div style="display: flex; gap: 8px;">
										<button type="button" class="small-btn"
											data-id="{{.ID}}"
											data-name="{{.Name}}"
											data-notification-key="{{.NotificationKey}}"
											data-host-secret="{{.HostSecret}}"
											data-webhook-secret="{{.WebhookSecret}}"
											data-endpoints="{{$first := true}}{{range .CallbackURLs}}{{if not $first}},{{end}}{{.}}{{$first = false}}{{end}}"
											data-allowlist="{{$first := true}}{{range .CallbackAllowlist}}{{if not $first}},{{end}}{{.}}{{$first = false}}{{end}}"
											onclick="triggerEditHost(this)"
											style="background: hsl(200, 85%, 35%);">✏️ Edit</button>
										<button type="button" class="small-btn" onclick="deleteHost('{{.ID}}')" style="background: hsl(0, 85%, 45%);">🗑️ Hapus</button>
									</div>
								</td>
							</tr>
							{{else}}
							<tr><td colspan="5">Belum ada host terdaftar.</td></tr>
							{{end}}
						</table>
					</div>
				</div>
			</div>

			<!-- PRODUCTS VIEW -->
			<div id="view-products" class="view-pane">
				<div id="products" class="section">
					<h2>Products</h2>
					<div class="table-wrap">
						<table>
							<tr><th>ID</th><th>Host ID</th><th>Nama</th><th>SKU</th><th>Harga</th><th>Active</th><th>Policy Override</th><th>Aksi</th></tr>
							{{range .Products}}
							<tr>
								<td><a href="/ui/product/{{.ID}}">{{.ID}}</a></td>
								<td>{{.HostID}}</td>
								<td>{{.Name}}</td>
								<td>{{.SKU}}</td>
								<td>{{.Price}}</td>
								<td>{{.IsActive}}</td>
								<td>{{if .FeePolicyOverride}}yes{{else}}no{{end}}</td>
								<td>
									<div style="display: flex; gap: 8px;">
										<button type="button" class="small-btn"
											data-id="{{.ID}}"
											data-host-id="{{.HostID}}"
											data-name="{{.Name}}"
											data-sku="{{.SKU}}"
											data-price="{{.Price}}"
											data-meta='{{json .Meta}}'
											data-override='{{if .FeePolicyOverride}}{{json .FeePolicyOverride}}{{end}}'
											data-active="{{.IsActive}}"
											onclick="triggerEditProduct(this)"
											style="background: hsl(200, 85%, 35%);">✏️ Edit</button>
										<button type="button" class="small-btn" onclick="deleteProduct('{{.ID}}')" style="background: hsl(0, 85%, 45%);">🗑️ Hapus</button>
									</div>
								</td>
							</tr>
							{{else}}
							<tr><td colspan="8">Belum ada produk terdaftar.</td></tr>
							{{end}}
						</table>
					</div>
				</div>
			</div>

			<!-- ORDERS VIEW -->
			<div id="view-orders" class="view-pane">
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
	<link rel="preconnect" href="https://fonts.googleapis.com">
	<link rel="preconnect" href="https://fonts.gstatic.com" crossorigin>
	<link href="https://fonts.googleapis.com/css2?family=Plus+Jakarta+Sans:wght@400;500;600;700&display=swap" rel="stylesheet">
	<style>
		:root {
			--primary: hsl(220, 85%, 32%);
			--primary-hover: hsl(220, 85%, 26%);
			--primary-light: hsl(220, 85%, 97%);
			--bg: hsl(210, 40%, 98%);
			--card-bg: #ffffff;
			--text: hsl(210, 40%, 15%);
			--text-muted: hsl(210, 15%, 45%);
			--border: hsl(210, 30%, 88%);
			--border-focus: hsl(220, 85%, 60%);
			--shadow-sm: 0 1px 2px 0 rgba(0, 0, 0, 0.05);
			--shadow: 0 4px 6px -1px rgba(0, 0, 0, 0.05), 0 2px 4px -1px rgba(0, 0, 0, 0.03);
			--shadow-lg: 0 10px 25px -5px rgba(15, 30, 60, 0.05), 0 20px 48px -10px rgba(15, 30, 60, 0.08);
			--radius: 12px;
			--radius-sm: 8px;
		}
		body { margin: 0; font-family: "Plus Jakarta Sans", -apple-system, BlinkMacSystemFont, "Segoe UI", Roboto, sans-serif; color: var(--text); background: var(--bg); }
		a { color: var(--primary); text-decoration: none; font-weight: 500; transition: color 0.2s ease; }
		a:hover { color: var(--primary-hover); text-decoration: underline; }
		h1, h2, h3 { margin-top: 0; font-weight: 700; color: var(--text); letter-spacing: -0.025em; }
		.admin-shell { display: grid; grid-template-columns: 270px minmax(0, 1fr); min-height: 100vh; }
		.sidebar { position: sticky; top: 0; height: 100vh; overflow-y: auto; padding: 32px 24px; background: linear-gradient(160deg, hsl(220, 85%, 20%), hsl(220, 85%, 15%)); color: #ecf3ff; display: flex; flex-direction: column; justify-content: space-between; }
		.sidebar h2 { margin: 0 0 8px 0; font-size: 20px; font-weight: 700; color: #ffffff; letter-spacing: -0.02em; }
		.sidebar nav { display: flex; flex-direction: column; gap: 8px; margin-top: 24px; }
		.sidebar a { color: hsl(220, 60%, 85%); text-decoration: none; font-size: 14px; font-weight: 500; border-radius: var(--radius-sm); padding: 12px 14px; transition: all 0.2s ease; }
		.sidebar a:hover { color: #ffffff; background: rgba(255, 255, 255, 0.08); }
		.sidebar .active { background: rgba(255, 255, 255, 0.15); color: #ffffff; font-weight: 600; }
		.sidebar .muted { margin-top: 16px; opacity: 0.7; font-size: 12px; line-height: 1.5; }
		.content { padding: 40px 32px; overflow-x: auto; max-width: 1400px; margin: 0 auto; width: 100%; box-sizing: border-box; }
		.section { background: var(--card-bg); border-radius: var(--radius); padding: 24px; margin-bottom: 24px; box-shadow: var(--shadow); border: 1px solid var(--border); transition: transform 0.2s ease, box-shadow 0.2s ease; }
		.section:hover { box-shadow: var(--shadow-lg); }
		.section h2 { margin-top: 0; margin-bottom: 16px; font-size: 18px; }
		table { border-collapse: collapse; width: 100%; margin-bottom: 20px; }
		td, th { border-bottom: 1px solid var(--border); padding: 12px 16px; text-align: left; vertical-align: middle; font-size: 14px; }
		tr:hover td { background-color: hsl(210, 40%, 99%); }
		th { background: hsl(210, 40%, 96%); color: var(--text); font-weight: 600; border-top: 1px solid var(--border); }
		pre { font-family: ui-monospace, SFMono-Regular, Menlo, Monaco, Consolas, monospace; font-size: 12px; background: hsl(210, 40%, 99%); padding: 12px; border: 1px solid var(--border); border-radius: var(--radius-sm); overflow-x: auto; margin: 0; color: hsl(210, 30%, 25%); text-align: left; }
		.hero { display: flex; gap: 24px; justify-content: space-between; align-items: center; padding: 32px; margin-bottom: 32px; border-radius: var(--radius); background: linear-gradient(135deg, hsl(220, 85%, 28%), hsl(220, 85%, 18%)); color: #eff5ff; box-shadow: var(--shadow-lg); }
		.hero h1 { margin: 0 0 8px 0; color: #fff; font-size: 30px; letter-spacing: -0.02em; }
		.subtle { color: rgba(239, 245, 255, 0.8); font-size: 14px; }
		.grid { display: grid; grid-template-columns: repeat(auto-fit, minmax(220px, 1fr)); gap: 16px; margin-bottom: 24px; }
		.grid .card { background: var(--card-bg); border: 1px solid var(--border); border-radius: var(--radius); padding: 20px; box-shadow: var(--shadow); }
		.grid .card strong { display: block; font-size: 13px; color: var(--text-muted); text-transform: uppercase; letter-spacing: 0.05em; margin-bottom: 8px; }
		.grid .card div { font-size: 20px; font-weight: 700; color: var(--primary); }
		.badge { display: inline-flex; align-items: center; padding: 4px 10px; font-size: 11px; font-weight: 700; border-radius: 9999px; text-transform: uppercase; letter-spacing: 0.05em; }
		.badge-success { background: hsl(120, 70%, 95%); color: hsl(120, 80%, 25%); }
		.badge-warning { background: hsl(40, 90%, 95%); color: hsl(40, 90%, 30%); }
		.badge-danger { background: hsl(0, 100%, 95%); color: hsl(0, 80%, 35%); }
		.badge-info { background: hsl(190, 90%, 95%); color: hsl(190, 90%, 30%); }
		.badge-secondary { background: hsl(210, 30%, 94%); color: hsl(210, 30%, 30%); }
		@media (max-width: 1024px) {
			.admin-shell { grid-template-columns: 1fr; }
			.sidebar { position: static; height: auto; padding: 24px; }
			.sidebar nav { flex-direction: row; flex-wrap: wrap; margin-top: 16px; gap: 8px; }
			.sidebar nav a { padding: 8px 12px; }
			.hero { flex-direction: column; align-items: flex-start; text-align: left; }
		}
	</style>
</head>
<body>
	<div class="admin-shell">
		<aside class="sidebar">
			<div class="sidebar-top">
				<h2>Host RuteBayar</h2>
				<p class="muted" style="margin-top:0;">Dashboard Admin</p>
				<nav>
					<a href="/ui">🏠 Dashboard</a>
					<a href="/ui/callbacks">🔁 Callback Monitor</a>
					<a href="/ui#hosts" class="active">📋 Hosts</a>
					<a href="/ui#products">🧩 Products</a>
					<a href="/ui#orders">🧾 Orders</a>
				</nav>
			</div>
			<div>
				<a href="/ui/logout" onclick="return confirm('Apakah Anda yakin ingin logout?');" style="display: inline-flex; align-items: center; gap: 8px; padding: 10px 14px; border-radius: var(--radius-sm); width: 100%; box-sizing: border-box; color: hsl(0, 100%, 80%); transition: all 0.2s ease;">🚪 Logout</a>
				<p class="muted" style="margin-top: 12px; margin-bottom: 0; font-size: 12px; opacity: 0.7;">Akses jalur ini untuk operasional lokal.</p>
			</div>
		</aside>
		<main class="content">
			<div class="hero">
				<div>
					<p class="subtle" style="text-transform: uppercase; letter-spacing: 0.12em; font-weight: 700; color: rgba(255, 255, 255, 0.7); margin-bottom: 4px;">Host detail</p>
					<h1>{{.Host.ID}}</h1>
					<p class="subtle">Informasi credential dan data operasional host.</p>
				</div>
				<a href="/ui" style="text-decoration:none;color:var(--primary);background:#fff;padding:10px 18px;border-radius:8px;font-weight:600;font-size:14px;box-shadow:var(--shadow-sm);transition:all 0.2s ease;">← Kembali ke Dashboard</a>
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
	<script>
		window.addEventListener("DOMContentLoaded", () => {
			// Format pre elements with JSON
			document.querySelectorAll("pre").forEach(pre => {
				try {
					const obj = JSON.parse(pre.textContent);
					pre.textContent = JSON.stringify(obj, null, 2);
				} catch(e) {}
			});

			// Map and render statuses with premium badges
			const cells = document.querySelectorAll("td");
			cells.forEach(c => {
				const text = c.textContent.trim().toUpperCase();
				if (text === "PAID" || text === "SUCCESS" || text === "OK") {
					c.innerHTML = '<span class="badge badge-success">' + text + '</span>';
				} else if (text === "PENDING" || text === "SUBMITTED") {
					c.innerHTML = '<span class="badge badge-warning">' + text + '</span>';
				} else if (text === "FAILED" || text === "BAD" || text === "EXPIRED") {
					c.innerHTML = '<span class="badge badge-danger">' + text + '</span>';
				} else if (text === "SANDBOX") {
					c.innerHTML = '<span class="badge badge-info">' + text + '</span>';
				} else if (text === "PRODUCTION" || text === "PROD") {
					c.innerHTML = '<span class="badge badge-secondary">' + text + '</span>';
				} else if (text === "TRUE") {
					c.innerHTML = '<span class="badge badge-success">ACTIVE</span>';
				} else if (text === "FALSE") {
					c.innerHTML = '<span class="badge badge-danger">INACTIVE</span>';
				}
			});
		});
	</script>
</body>
</html>`

const uiProductHTML = `<!doctype html>
<html>
<head>
	<meta charset="utf-8"/>
	<title>Product {{.Product.ID}}</title>
	<link rel="preconnect" href="https://fonts.googleapis.com">
	<link rel="preconnect" href="https://fonts.gstatic.com" crossorigin>
	<link href="https://fonts.googleapis.com/css2?family=Plus+Jakarta+Sans:wght@400;500;600;700&display=swap" rel="stylesheet">
	<style>
		:root {
			--primary: hsl(220, 85%, 32%);
			--primary-hover: hsl(220, 85%, 26%);
			--primary-light: hsl(220, 85%, 97%);
			--bg: hsl(210, 40%, 98%);
			--card-bg: #ffffff;
			--text: hsl(210, 40%, 15%);
			--text-muted: hsl(210, 15%, 45%);
			--border: hsl(210, 30%, 88%);
			--border-focus: hsl(220, 85%, 60%);
			--shadow-sm: 0 1px 2px 0 rgba(0, 0, 0, 0.05);
			--shadow: 0 4px 6px -1px rgba(0, 0, 0, 0.05), 0 2px 4px -1px rgba(0, 0, 0, 0.03);
			--shadow-lg: 0 10px 25px -5px rgba(15, 30, 60, 0.05), 0 20px 48px -10px rgba(15, 30, 60, 0.08);
			--radius: 12px;
			--radius-sm: 8px;
		}
		body { margin: 0; font-family: "Plus Jakarta Sans", -apple-system, BlinkMacSystemFont, "Segoe UI", Roboto, sans-serif; color: var(--text); background: var(--bg); }
		a { color: var(--primary); text-decoration: none; font-weight: 500; transition: color 0.2s ease; }
		a:hover { color: var(--primary-hover); text-decoration: underline; }
		h1, h2, h3 { margin-top: 0; font-weight: 700; color: var(--text); letter-spacing: -0.025em; }
		.admin-shell { display: grid; grid-template-columns: 270px minmax(0, 1fr); min-height: 100vh; }
		.sidebar { position: sticky; top: 0; height: 100vh; overflow-y: auto; padding: 32px 24px; background: linear-gradient(160deg, hsl(220, 85%, 20%), hsl(220, 85%, 15%)); color: #ecf3ff; display: flex; flex-direction: column; justify-content: space-between; }
		.sidebar h2 { margin: 0 0 8px 0; font-size: 20px; font-weight: 700; color: #ffffff; letter-spacing: -0.02em; }
		.sidebar nav { display: flex; flex-direction: column; gap: 8px; margin-top: 24px; }
		.sidebar a { color: hsl(220, 60%, 85%); text-decoration: none; font-size: 14px; font-weight: 500; border-radius: var(--radius-sm); padding: 12px 14px; transition: all 0.2s ease; }
		.sidebar a:hover { color: #ffffff; background: rgba(255, 255, 255, 0.08); }
		.sidebar .active { background: rgba(255, 255, 255, 0.15); color: #ffffff; font-weight: 600; }
		.sidebar .muted { margin-top: 16px; opacity: 0.7; font-size: 12px; line-height: 1.5; }
		.content { padding: 40px 32px; overflow-x: auto; max-width: 1400px; margin: 0 auto; width: 100%; box-sizing: border-box; }
		.section { background: var(--card-bg); border-radius: var(--radius); padding: 24px; margin-bottom: 24px; box-shadow: var(--shadow); border: 1px solid var(--border); transition: transform 0.2s ease, box-shadow 0.2s ease; }
		.section:hover { box-shadow: var(--shadow-lg); }
		.section h2 { margin-top: 0; margin-bottom: 16px; font-size: 18px; }
		table { border-collapse: collapse; width: 100%; margin-bottom: 20px; }
		td, th { border-bottom: 1px solid var(--border); padding: 12px 16px; text-align: left; vertical-align: middle; font-size: 14px; }
		tr:hover td { background-color: hsl(210, 40%, 99%); }
		th { background: hsl(210, 40%, 96%); color: var(--text); font-weight: 600; border-top: 1px solid var(--border); }
		pre { font-family: ui-monospace, SFMono-Regular, Menlo, Monaco, Consolas, monospace; font-size: 12px; background: hsl(210, 40%, 99%); padding: 12px; border: 1px solid var(--border); border-radius: var(--radius-sm); overflow-x: auto; margin: 0; color: hsl(210, 30%, 25%); text-align: left; }
		.hero { display: flex; gap: 24px; justify-content: space-between; align-items: center; padding: 32px; margin-bottom: 32px; border-radius: var(--radius); background: linear-gradient(135deg, hsl(220, 85%, 28%), hsl(220, 85%, 18%)); color: #eff5ff; box-shadow: var(--shadow-lg); }
		.hero h1 { margin: 0 0 8px 0; color: #fff; font-size: 30px; letter-spacing: -0.02em; }
		.subtle { color: rgba(239, 245, 255, 0.8); font-size: 14px; }
		.badge { display: inline-flex; align-items: center; padding: 4px 10px; font-size: 11px; font-weight: 700; border-radius: 9999px; text-transform: uppercase; letter-spacing: 0.05em; }
		.badge-success { background: hsl(120, 70%, 95%); color: hsl(120, 80%, 25%); }
		.badge-danger { background: hsl(0, 100%, 95%); color: hsl(0, 80%, 35%); }
		@media (max-width: 1024px) {
			.admin-shell { grid-template-columns: 1fr; }
			.sidebar { position: static; height: auto; padding: 24px; }
			.sidebar nav { flex-direction: row; flex-wrap: wrap; margin-top: 16px; gap: 8px; }
			.sidebar nav a { padding: 8px 12px; }
			.hero { flex-direction: column; align-items: flex-start; text-align: left; }
		}
	</style>
</head>
<body>
	<div class="admin-shell">
		<aside class="sidebar">
			<div class="sidebar-top">
				<h2>Host RuteBayar</h2>
				<p class="muted" style="margin-top:0;">Dashboard Admin</p>
				<nav>
					<a href="/ui">🏠 Dashboard</a>
					<a href="/ui/callbacks">🔁 Callback Monitor</a>
					<a href="/ui#hosts">📋 Hosts</a>
					<a href="/ui#products" class="active">🧩 Products</a>
					<a href="/ui#orders">🧾 Orders</a>
				</nav>
			</div>
			<div>
				<a href="/ui/logout" onclick="return confirm('Apakah Anda yakin ingin logout?');" style="display: inline-flex; align-items: center; gap: 8px; padding: 10px 14px; border-radius: var(--radius-sm); width: 100%; box-sizing: border-box; color: hsl(0, 100%, 80%); transition: all 0.2s ease;">🚪 Logout</a>
				<p class="muted" style="margin-top: 12px; margin-bottom: 0; font-size: 12px; opacity: 0.7;">Akses jalur ini untuk operasional lokal.</p>
			</div>
		</aside>
		<main class="content">
			<div class="hero">
				<div>
					<p class="subtle" style="text-transform: uppercase; letter-spacing: 0.12em; font-weight: 700; color: rgba(255, 255, 255, 0.7); margin-bottom: 4px;">Product detail</p>
					<h1>{{.Product.ID}}</h1>
					<p class="subtle">Lengkap dengan relasi host dan konfigurasi fee override.</p>
				</div>
				<a href="/ui" style="text-decoration:none;color:var(--primary);background:#fff;padding:10px 18px;border-radius:8px;font-weight:600;font-size:14px;box-shadow:var(--shadow-sm);transition:all 0.2s ease;">← Kembali ke Dashboard</a>
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
	<script>
		window.addEventListener("DOMContentLoaded", () => {
			// Format pre elements with JSON
			document.querySelectorAll("pre").forEach(pre => {
				try {
					const obj = JSON.parse(pre.textContent);
					pre.textContent = JSON.stringify(obj, null, 2);
				} catch(e) {}
			});

			// Map active badges
			const cells = document.querySelectorAll("td");
			cells.forEach(td => {
				const text = td.textContent.trim().toUpperCase();
				if (text === "TRUE") {
					td.innerHTML = '<span class="badge badge-success">ACTIVE</span>';
				} else if (text === "FALSE") {
					td.innerHTML = '<span class="badge badge-danger">INACTIVE</span>';
				}
			});
		});
	</script>
</body>
</html>`

const uiOrderHTML = `<!doctype html>
<html>
<head>
	<meta charset="utf-8"/>
	<title>Order {{.Order.Reference}}</title>
	<link rel="preconnect" href="https://fonts.googleapis.com">
	<link rel="preconnect" href="https://fonts.gstatic.com" crossorigin>
	<link href="https://fonts.googleapis.com/css2?family=Plus+Jakarta+Sans:wght@400;500;600;700&display=swap" rel="stylesheet">
	<style>
		:root {
			--primary: hsl(220, 85%, 32%);
			--primary-hover: hsl(220, 85%, 26%);
			--primary-light: hsl(220, 85%, 97%);
			--bg: hsl(210, 40%, 98%);
			--card-bg: #ffffff;
			--text: hsl(210, 40%, 15%);
			--text-muted: hsl(210, 15%, 45%);
			--border: hsl(210, 30%, 88%);
			--border-focus: hsl(220, 85%, 60%);
			--shadow-sm: 0 1px 2px 0 rgba(0, 0, 0, 0.05);
			--shadow: 0 4px 6px -1px rgba(0, 0, 0, 0.05), 0 2px 4px -1px rgba(0, 0, 0, 0.03);
			--shadow-lg: 0 10px 25px -5px rgba(15, 30, 60, 0.05), 0 20px 48px -10px rgba(15, 30, 60, 0.08);
			--radius: 12px;
			--radius-sm: 8px;
		}
		body { margin: 0; font-family: "Plus Jakarta Sans", -apple-system, BlinkMacSystemFont, "Segoe UI", Roboto, sans-serif; color: var(--text); background: var(--bg); }
		a { color: var(--primary); text-decoration: none; font-weight: 500; transition: color 0.2s ease; }
		a:hover { color: var(--primary-hover); text-decoration: underline; }
		h1, h2, h3 { margin-top: 0; font-weight: 700; color: var(--text); letter-spacing: -0.025em; }
		.admin-shell { display: grid; grid-template-columns: 270px minmax(0, 1fr); min-height: 100vh; }
		.sidebar { position: sticky; top: 0; height: 100vh; overflow-y: auto; padding: 32px 24px; background: linear-gradient(160deg, hsl(220, 85%, 20%), hsl(220, 85%, 15%)); color: #ecf3ff; display: flex; flex-direction: column; justify-content: space-between; }
		.sidebar h2 { margin: 0 0 8px 0; font-size: 20px; font-weight: 700; color: #ffffff; letter-spacing: -0.02em; }
		.sidebar nav { display: flex; flex-direction: column; gap: 8px; margin-top: 24px; }
		.sidebar a { color: hsl(220, 60%, 85%); text-decoration: none; font-size: 14px; font-weight: 500; border-radius: var(--radius-sm); padding: 12px 14px; transition: all 0.2s ease; }
		.sidebar a:hover { color: #ffffff; background: rgba(255, 255, 255, 0.08); }
		.sidebar .active { background: rgba(255, 255, 255, 0.15); color: #ffffff; font-weight: 600; }
		.sidebar .muted { margin-top: 16px; opacity: 0.7; font-size: 12px; line-height: 1.5; }
		.content { padding: 40px 32px; overflow-x: auto; max-width: 1400px; margin: 0 auto; width: 100%; box-sizing: border-box; }
		.section { background: var(--card-bg); border-radius: var(--radius); padding: 24px; margin-bottom: 24px; box-shadow: var(--shadow); border: 1px solid var(--border); transition: transform 0.2s ease, box-shadow 0.2s ease; }
		.section:hover { box-shadow: var(--shadow-lg); }
		.section h2 { margin-top: 0; margin-bottom: 16px; font-size: 18px; }
		table { border-collapse: collapse; width: 100%; margin-bottom: 20px; }
		td, th { border-bottom: 1px solid var(--border); padding: 12px 16px; text-align: left; vertical-align: middle; font-size: 14px; }
		tr:hover td { background-color: hsl(210, 40%, 99%); }
		th { background: hsl(210, 40%, 96%); color: var(--text); font-weight: 600; border-top: 1px solid var(--border); }
		pre { font-family: ui-monospace, SFMono-Regular, Menlo, Monaco, Consolas, monospace; font-size: 12px; background: hsl(210, 40%, 99%); padding: 12px; border: 1px solid var(--border); border-radius: var(--radius-sm); overflow-x: auto; margin: 0; color: hsl(210, 30%, 25%); text-align: left; }
		.hero { display: flex; gap: 24px; justify-content: space-between; align-items: center; padding: 32px; margin-bottom: 32px; border-radius: var(--radius); background: linear-gradient(135deg, hsl(220, 85%, 28%), hsl(220, 85%, 18%)); color: #eff5ff; box-shadow: var(--shadow-lg); }
		.hero h1 { margin: 0 0 8px 0; color: #fff; font-size: 30px; letter-spacing: -0.02em; }
		.subtle { color: rgba(239, 245, 255, 0.8); font-size: 14px; }
		.badge { display: inline-flex; align-items: center; padding: 4px 10px; font-size: 11px; font-weight: 700; border-radius: 9999px; text-transform: uppercase; letter-spacing: 0.05em; }
		.badge-success { background: hsl(120, 70%, 95%); color: hsl(120, 80%, 25%); }
		.badge-warning { background: hsl(40, 90%, 95%); color: hsl(40, 90%, 30%); }
		.badge-danger { background: hsl(0, 100%, 95%); color: hsl(0, 80%, 35%); }
		.badge-info { background: hsl(190, 90%, 95%); color: hsl(190, 90%, 30%); }
		.badge-secondary { background: hsl(210, 30%, 94%); color: hsl(210, 30%, 30%); }
		@media (max-width: 1024px) {
			.admin-shell { grid-template-columns: 1fr; }
			.sidebar { position: static; height: auto; padding: 24px; }
			.sidebar nav { flex-direction: row; flex-wrap: wrap; margin-top: 16px; gap: 8px; }
			.sidebar nav a { padding: 8px 12px; }
			.hero { flex-direction: column; align-items: flex-start; text-align: left; }
		}
	</style>
</head>
<body>
	<div class="admin-shell">
		<aside class="sidebar">
			<div class="sidebar-top">
				<h2>Host RuteBayar</h2>
				<p class="muted" style="margin-top:0;">Dashboard Admin</p>
				<nav>
					<a href="/ui">🏠 Dashboard</a>
					<a href="/ui/callbacks">🔁 Callback Monitor</a>
					<a href="/ui#hosts">📋 Hosts</a>
					<a href="/ui#products">🧩 Products</a>
					<a href="/ui#orders" class="active">🧾 Orders</a>
				</nav>
			</div>
			<div>
				<a href="/ui/logout" onclick="return confirm('Apakah Anda yakin ingin logout?');" style="display: inline-flex; align-items: center; gap: 8px; padding: 10px 14px; border-radius: var(--radius-sm); width: 100%; box-sizing: border-box; color: hsl(0, 100%, 80%); transition: all 0.2s ease;">🚪 Logout</a>
				<p class="muted" style="margin-top: 12px; margin-bottom: 0; font-size: 12px; opacity: 0.7;">Akses jalur ini untuk operasional lokal.</p>
			</div>
		</aside>
		<main class="content">
			<div class="hero">
				<div>
					<p class="subtle" style="text-transform: uppercase; letter-spacing: 0.12em; font-weight: 700; color: rgba(255, 255, 255, 0.7); margin-bottom: 4px;">Order detail</p>
					<h1>{{.Order.Reference}}</h1>
					<p class="subtle">Riwayat status dan data settlement untuk transaksi ini.</p>
				</div>
				<a href="/ui" style="text-decoration:none;color:var(--primary);background:#fff;padding:10px 18px;border-radius:8px;font-weight:600;font-size:14px;box-shadow:var(--shadow-sm);transition:all 0.2s ease;">← Kembali ke Dashboard</a>
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
				<p class="subtle">Ledger belum terbentuk.</p>
				{{end}}
			</div>
		</main>
	</div>
	<script>
		window.addEventListener("DOMContentLoaded", () => {
			// Map and render statuses with premium badges
			const cells = document.querySelectorAll("td");
			cells.forEach(td => {
				const text = td.textContent.trim().toUpperCase();
				if (text === "PAID" || text === "SUCCESS" || text === "OK") {
					td.innerHTML = '<span class="badge badge-success">' + text + '</span>';
				} else if (text === "PENDING" || text === "SUBMITTED") {
					td.innerHTML = '<span class="badge badge-warning">' + text + '</span>';
				} else if (text === "FAILED" || text === "BAD" || text === "EXPIRED") {
					td.innerHTML = '<span class="badge badge-danger">' + text + '</span>';
				} else if (text === "SANDBOX") {
					td.innerHTML = '<span class="badge badge-info">' + text + '</span>';
				} else if (text === "PRODUCTION" || text === "PROD") {
					td.innerHTML = '<span class="badge badge-secondary">' + text + '</span>';
				}
			});
		});
	</script>
</body>
</html>`

const uiCallbacksHTML = `<!doctype html>
<html>
<head>
	<meta charset="utf-8"/>
	<title>Callback monitor</title>
	<link rel="preconnect" href="https://fonts.googleapis.com">
	<link rel="preconnect" href="https://fonts.gstatic.com" crossorigin>
	<link href="https://fonts.googleapis.com/css2?family=Plus+Jakarta+Sans:wght@400;500;600;700&display=swap" rel="stylesheet">
	<style>
		:root {
			--primary: hsl(220, 85%, 32%);
			--primary-hover: hsl(220, 85%, 26%);
			--primary-light: hsl(220, 85%, 97%);
			--bg: hsl(210, 40%, 98%);
			--card-bg: #ffffff;
			--text: hsl(210, 40%, 15%);
			--text-muted: hsl(210, 15%, 45%);
			--border: hsl(210, 30%, 88%);
			--border-focus: hsl(220, 85%, 60%);
			--shadow-sm: 0 1px 2px 0 rgba(0, 0, 0, 0.05);
			--shadow: 0 4px 6px -1px rgba(0, 0, 0, 0.05), 0 2px 4px -1px rgba(0, 0, 0, 0.03);
			--shadow-lg: 0 10px 25px -5px rgba(15, 30, 60, 0.05), 0 20px 48px -10px rgba(15, 30, 60, 0.08);
			--radius: 12px;
			--radius-sm: 8px;
		}
		body { font-family: "Plus Jakarta Sans", -apple-system, BlinkMacSystemFont, "Segoe UI", Roboto, sans-serif; margin: 0; color: var(--text); background: var(--bg); }
		.admin-shell { display: grid; grid-template-columns: 270px minmax(0, 1fr); min-height: 100vh; }
		.sidebar { position: sticky; top: 0; height: 100vh; overflow-y: auto; padding: 32px 24px; background: linear-gradient(160deg, hsl(220, 85%, 20%), hsl(220, 85%, 15%)); color: #ecf3ff; display: flex; flex-direction: column; justify-content: space-between; }
		.sidebar h2 { margin: 0 0 8px 0; font-size: 20px; font-weight: 700; color: #ffffff; letter-spacing: -0.02em; }
		.sidebar nav { display: flex; flex-direction: column; gap: 8px; margin-top: 24px; }
		.sidebar a { color: hsl(220, 60%, 85%); text-decoration: none; font-size: 14px; font-weight: 500; border-radius: var(--radius-sm); padding: 12px 14px; transition: all 0.2s ease; }
		.sidebar a:hover { color: #ffffff; background: rgba(255, 255, 255, 0.08); }
		.sidebar .active { background: rgba(255, 255, 255, 0.15); color: #ffffff; font-weight: 600; }
		.sidebar .muted { margin-top: 16px; opacity: 0.7; font-size: 12px; line-height: 1.5; }
		.content { padding: 40px 32px; overflow-x: auto; max-width: 1400px; margin: 0 auto; width: 100%; box-sizing: border-box; }
		.eyebrow { margin: 0 0 6px 0; text-transform: uppercase; letter-spacing: 0.12em; font-size: 11px; color: rgba(255, 255, 255, 0.7); font-weight: 700; }
		.hero { display: flex; justify-content: space-between; align-items: center; gap: 24px; margin-bottom: 32px; padding: 32px; border-radius: var(--radius); background: linear-gradient(135deg, hsl(220, 85%, 28%), hsl(220, 85%, 18%)); color: #eff5ff; box-shadow: var(--shadow-lg); }
		.hero h1 { margin: 0 0 8px 0; font-size: 30px; letter-spacing: -0.025em; color: #fff; }
		.hero .subtle { color: rgba(239, 245, 255, 0.8); max-width: 820px; font-size: 15px; }
		.hero-actions { display: flex; gap: 12px; flex-wrap: wrap; }
		.button-link, .ghost-link { display: inline-flex; align-items: center; justify-content: center; gap: 8px; padding: 10px 18px; border-radius: var(--radius-sm); text-decoration: none; font-weight: 600; font-size: 14px; transition: all 0.2s ease; cursor: pointer; border: 0; }
		.button-link { background: #fff; color: hsl(220, 85%, 25%); box-shadow: var(--shadow-sm); }
		.button-link:hover { transform: translateY(-1px); box-shadow: var(--shadow); background: hsl(210, 40%, 98%); }
		.ghost-link { color: #eff5ff; border: 1px solid rgba(255, 255, 255, 0.2); background: rgba(255, 255, 255, 0.06); }
		.ghost-link:hover { background: rgba(255, 255, 255, 0.12); border-color: rgba(255, 255, 255, 0.3); }
		.table-wrap { overflow-x: auto; margin-top: 16px; border: 1px solid var(--border); border-radius: var(--radius-sm); background: var(--card-bg); }
		table { border-collapse: collapse; width: 100%; }
		td, th { border-bottom: 1px solid var(--border); padding: 12px 16px; text-align: left; vertical-align: middle; font-size: 14px; }
		tr:hover td { background-color: hsl(210, 40%, 99%); }
		th { background: hsl(210, 40%, 96%); color: var(--text); font-weight: 600; border-top: 1px solid var(--border); }
		.section { background: var(--card-bg); border-radius: var(--radius); padding: 24px; box-shadow: var(--shadow); border: 1px solid var(--border); }
		a { color: var(--primary); text-decoration: none; font-weight: 500; transition: color 0.2s ease; }
		a:hover { color: var(--primary-hover); text-decoration: underline; }
		.row-result { font-family: ui-monospace, SFMono-Regular, Menlo, Monaco, Consolas, monospace; font-size: 12px; color: hsl(210, 15%, 35%); }
		button { cursor: pointer; border: 0; background: var(--primary); color: #fff; border-radius: var(--radius-sm); padding: 8px 14px; font-weight: 600; font-family: inherit; font-size: 13px; transition: all 0.2s ease; }
		button:hover:not(:disabled) { background: var(--primary-hover); transform: translateY(-1px); box-shadow: var(--shadow-sm); }
		button:active:not(:disabled) { transform: translateY(0); }
		button:disabled { opacity: 0.5; cursor: not-allowed; }
		pre { font-family: ui-monospace, SFMono-Regular, Menlo, Monaco, Consolas, monospace; font-size: 12px; background: hsl(210, 40%, 99%); padding: 12px; border: 1px solid var(--border); border-radius: var(--radius-sm); overflow-x: auto; margin: 0; color: hsl(210, 30%, 25%); text-align: left; }
		.badge { display: inline-flex; align-items: center; padding: 4px 10px; font-size: 11px; font-weight: 700; border-radius: 9999px; text-transform: uppercase; letter-spacing: 0.05em; }
		.badge-success { background: hsl(120, 70%, 95%); color: hsl(120, 80%, 25%); }
		.badge-danger { background: hsl(0, 100%, 95%); color: hsl(0, 80%, 35%); }
		.badge-warning { background: hsl(40, 90%, 95%); color: hsl(40, 90%, 30%); }
		.badge-info { background: hsl(190, 90%, 95%); color: hsl(190, 90%, 30%); }
		.badge-secondary { background: hsl(210, 30%, 94%); color: hsl(210, 30%, 30%); }
		@media (max-width: 1024px) {
			.admin-shell { grid-template-columns: 1fr; }
			.sidebar { position: static; height: auto; padding: 24px; }
			.sidebar nav { flex-direction: row; flex-wrap: wrap; gap: 8px; }
			.sidebar nav a { padding: 8px 12px; }
			.hero { flex-direction: column; align-items: flex-start; text-align: left; }
			.hero-actions { justify-content: flex-start; width: 100%; margin-top: 12px; }
		}
	</style>
	<script>
		function showToast(message, type = "success") {
			let container = document.getElementById("toast-container");
			if (!container) {
				container = document.createElement("div");
				container.id = "toast-container";
				container.style.position = "fixed";
				container.style.top = "24px";
				container.style.right = "24px";
				container.style.zIndex = "9999";
				container.style.display = "flex";
				container.style.flexDirection = "column";
				container.style.gap = "8px";
				document.body.appendChild(container);
			}
			const toast = document.createElement("div");
			toast.style.background = type === "success" ? "hsl(120, 70%, 25%)" : "hsl(0, 80%, 35%)";
			toast.style.color = "#fff";
			toast.style.padding = "12px 20px";
			toast.style.borderRadius = "8px";
			toast.style.boxShadow = "0 4px 12px rgba(0,0,0,0.15)";
			toast.style.fontFamily = "inherit";
			toast.style.fontSize = "14px";
			toast.style.fontWeight = "600";
			toast.style.minWidth = "280px";
			toast.style.transition = "all 0.3s ease";
			toast.style.opacity = "0";
			toast.style.transform = "translateY(-20px)";
			toast.textContent = message;

			container.appendChild(toast);

			setTimeout(() => {
				toast.style.opacity = "1";
				toast.style.transform = "translateY(0)";
			}, 10);

			setTimeout(() => {
				toast.style.opacity = "0";
				toast.style.transform = "translateY(-20px)";
				setTimeout(() => toast.remove(), 300);
			}, 3500);
		}

		async function replayFromButton(buttonRef) {
			const { reference, provider, status, idempotencyKey } = buttonRef.dataset;
			if (!reference || !provider || !status || !idempotencyKey) {
				showToast("Reference, provider, status, dan idempotency key wajib ada.", "error");
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
					showToast("Replay gagal: " + payload, "error");
					buttonRef.disabled = false;
					return;
				}
				showToast("Replay berhasil dikirim!", "success");
				setTimeout(() => location.reload(), 1000);
			} catch (err) {
				showToast("Replay gagal: " + err.message, "error");
				buttonRef.disabled = false;
			}
		}

		window.addEventListener("DOMContentLoaded", () => {
			// Format pre elements with JSON
			document.querySelectorAll("pre").forEach(pre => {
				try {
					const obj = JSON.parse(pre.textContent);
					pre.textContent = JSON.stringify(obj, null, 2);
				} catch(e) {}
			});

			// Map status/results to badges
			const cells = document.querySelectorAll("td");
			cells.forEach(td => {
				const text = td.textContent.trim().toLowerCase();
				if (text === "success" || text === "ok" || text === "paid") {
					td.innerHTML = '<span class="badge badge-success">' + text + '</span>';
				} else if (text === "failed" || text === "bad" || text === "expired") {
					td.innerHTML = '<span class="badge badge-danger">' + text + '</span>';
				} else if (text === "pending" || text === "submitted") {
					td.innerHTML = '<span class="badge badge-warning">' + text + '</span>';
				} else if (text === "sandbox") {
					td.innerHTML = '<span class="badge badge-info">' + text + '</span>';
				} else if (text === "production" || text === "prod") {
					td.innerHTML = '<span class="badge badge-secondary">' + text + '</span>';
				}
			});
		});
	</script>
</head>
<body>
	<div class="admin-shell">
		<aside class="sidebar">
			<div class="sidebar-top">
				<h2>Host RuteBayar</h2>
				<p class="muted" style="margin-top:0;">Dashboard Admin</p>
				<nav>
					<a href="/ui">🏠 Dashboard</a>
					<a href="/ui/callbacks" class="active">🔁 Callback Monitor</a>
					<a href="/ui#hosts">📋 Hosts</a>
					<a href="/ui#products">🧩 Products</a>
					<a href="/ui#orders">🧾 Orders</a>
				</nav>
			</div>
			<div>
				<a href="/ui/logout" onclick="return confirm('Apakah Anda yakin ingin logout?');" style="display: inline-flex; align-items: center; gap: 8px; padding: 10px 14px; border-radius: var(--radius-sm); width: 100%; box-sizing: border-box; color: hsl(0, 100%, 80%); transition: all 0.2s ease;">🚪 Logout</a>
				<p class="muted" style="margin-top: 12px; margin-bottom: 0; font-size: 12px; opacity: 0.7;">Monitor untuk delivery callback dan replay event.</p>
			</div>
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
							<td>{{.Result}}</td>
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
