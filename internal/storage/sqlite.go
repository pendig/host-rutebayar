package storage

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"time"

	_ "modernc.org/sqlite"

	"github.com/pendig/host-rutebayar/internal/domain"
)

const defaultInMemoryDSN = "file::memory:?cache=shared"

// SQLiteStore persists host/rule/order data for self-hosted deployments.
type SQLiteStore struct {
	db *sql.DB
}

// NewSQLiteStore opens sqlite database and returns a store handle.
func NewSQLiteStore(dsn string) (*SQLiteStore, error) {
	if dsn == "" {
		dsn = defaultInMemoryDSN
	}
	db, err := sql.Open("sqlite", dsn)
	if err != nil {
		return nil, fmt.Errorf("open sqlite db: %w", err)
	}
	if err := db.PingContext(context.Background()); err != nil {
		_ = db.Close()
		return nil, fmt.Errorf("sqlite ping: %w", err)
	}
	return &SQLiteStore{db: db}, nil
}

// DB returns internal database connection.
func (s *SQLiteStore) DB() *sql.DB {
	return s.db
}

// Migrate creates schema used by host-rutebayar.
func (s *SQLiteStore) Migrate() error {
	ddl := []string{
		`CREATE TABLE IF NOT EXISTS hosts (
			id TEXT PRIMARY KEY,
			name TEXT NOT NULL,
			callback_urls TEXT,
			callback_allowlist TEXT,
			notification_key TEXT NOT NULL,
			host_secret TEXT NOT NULL,
			webhook_secret TEXT NOT NULL,
			created_at TIMESTAMP NOT NULL,
			updated_at TIMESTAMP NOT NULL
		);`,
		`CREATE TABLE IF NOT EXISTS host_fee_policies (
			host_id TEXT PRIMARY KEY,
			type TEXT NOT NULL,
			value REAL NOT NULL,
			currency TEXT NOT NULL,
			rounding TEXT NOT NULL,
			min_fee INTEGER,
			max_fee INTEGER,
			policy_version TEXT,
			updated_at TIMESTAMP NOT NULL,
			FOREIGN KEY(host_id) REFERENCES hosts(id)
		);`,
		`CREATE TABLE IF NOT EXISTS host_provider_accounts (
			host_id TEXT NOT NULL,
			provider TEXT NOT NULL,
			env TEXT NOT NULL,
			credentials_hash TEXT NOT NULL,
			public_config TEXT,
			created_at TIMESTAMP NOT NULL,
			PRIMARY KEY(host_id, provider, env),
			FOREIGN KEY(host_id) REFERENCES hosts(id)
		);`,
		`CREATE TABLE IF NOT EXISTS products (
			id TEXT PRIMARY KEY,
			host_id TEXT NOT NULL,
			name TEXT NOT NULL,
			sku TEXT,
			price INTEGER NOT NULL,
			is_active INTEGER NOT NULL,
			meta TEXT,
			policy_type TEXT,
			policy_value REAL,
			policy_currency TEXT,
			policy_rounding TEXT,
			policy_min_fee INTEGER,
			policy_max_fee INTEGER,
			policy_version TEXT,
			created_at TIMESTAMP NOT NULL,
			updated_at TIMESTAMP NOT NULL,
			FOREIGN KEY(host_id) REFERENCES hosts(id)
		);`,
		`CREATE TABLE IF NOT EXISTS payment_orders (
			id INTEGER PRIMARY KEY AUTOINCREMENT,
			reference TEXT UNIQUE NOT NULL,
			host_id TEXT NOT NULL,
			product_id TEXT NOT NULL,
			provider TEXT NOT NULL,
			provider_reference TEXT,
			provider_checkout_url TEXT,
			currency TEXT NOT NULL,
			env TEXT NOT NULL,
			status TEXT NOT NULL,
			gross_amount INTEGER NOT NULL,
			provider_fee_amount INTEGER NOT NULL,
			host_fee_amount INTEGER NOT NULL,
			net_amount INTEGER NOT NULL,
			buyer_ref TEXT,
			policy_snapshot_id TEXT,
			created_at TIMESTAMP NOT NULL,
			updated_at TIMESTAMP NOT NULL,
			FOREIGN KEY(host_id) REFERENCES hosts(id),
			FOREIGN KEY(product_id) REFERENCES products(id)
		);`,
		`CREATE TABLE IF NOT EXISTS payment_order_ledgers (
			id INTEGER PRIMARY KEY AUTOINCREMENT,
			payment_order_id INTEGER NOT NULL,
			gross_amount INTEGER NOT NULL,
			host_fee_amount INTEGER NOT NULL,
			provider_fee_amount INTEGER NOT NULL,
			net_amount INTEGER NOT NULL,
			policy_checksum TEXT NOT NULL,
			idempotency_key TEXT UNIQUE NOT NULL,
			created_at TIMESTAMP NOT NULL,
			FOREIGN KEY(payment_order_id) REFERENCES payment_orders(id)
		);`,
		`CREATE UNIQUE INDEX IF NOT EXISTS idx_payment_orders_ref ON payment_orders(reference);`,
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	for _, stmt := range ddl {
		if _, err := s.db.ExecContext(ctx, stmt); err != nil {
			return fmt.Errorf("sqlite migrate: %w", err)
		}
	}
	return nil
}

// UpsertHost creates or updates host metadata.
func (s *SQLiteStore) UpsertHost(host domain.Host) error {
	now := time.Now().UTC()
	_, err := s.db.Exec(`INSERT INTO hosts (id, name, callback_urls, callback_allowlist, notification_key, host_secret, webhook_secret, created_at, updated_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)
		ON CONFLICT(id) DO UPDATE SET
			name=excluded.name,
			callback_urls=excluded.callback_urls,
			callback_allowlist=excluded.callback_allowlist,
			notification_key=excluded.notification_key,
			host_secret=excluded.host_secret,
			webhook_secret=excluded.webhook_secret,
			updated_at=excluded.updated_at;`,
		host.ID, host.Name, jsonString(host.CallbackURLs), jsonString(host.CallbackAllowlist), host.NotificationKey, host.HostSecret, host.WebhookSecret, now, now)
	return err
}

// GetHost returns host by id.
func (s *SQLiteStore) GetHost(hostID string) (domain.Host, error) {
	var (
		host        domain.Host
		callbackRaw sql.NullString
		allowRaw    sql.NullString
	)
	err := s.db.QueryRow(`SELECT id, name, callback_urls, callback_allowlist, notification_key, host_secret, webhook_secret
		FROM hosts
		WHERE id = ?`, hostID).
		Scan(&host.ID, &host.Name, &callbackRaw, &allowRaw, &host.NotificationKey, &host.HostSecret, &host.WebhookSecret)
	if err == sql.ErrNoRows {
		return domain.Host{}, fmt.Errorf("host not found")
	}
	if err != nil {
		return domain.Host{}, err
	}
	host.CallbackURLs = parseStringSlice(callbackRaw.String)
	host.CallbackAllowlist = parseStringSlice(allowRaw.String)
	return host, nil
}

// ListHosts returns all hosts.
func (s *SQLiteStore) ListHosts() ([]domain.Host, error) {
	rows, err := s.db.Query(`SELECT id, name, callback_urls, callback_allowlist, notification_key, host_secret, webhook_secret
		FROM hosts ORDER BY id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	hosts := []domain.Host{}
	for rows.Next() {
		var host domain.Host
		var callbackRaw sql.NullString
		var allowRaw sql.NullString
		if err := rows.Scan(&host.ID, &host.Name, &callbackRaw, &allowRaw, &host.NotificationKey, &host.HostSecret, &host.WebhookSecret); err != nil {
			return nil, err
		}
		host.CallbackURLs = parseStringSlice(callbackRaw.String)
		host.CallbackAllowlist = parseStringSlice(allowRaw.String)
		hosts = append(hosts, host)
	}
	return hosts, rows.Err()
}

// UpsertHostPolicy stores host fee policy.
func (s *SQLiteStore) UpsertHostPolicy(hostID string, policy domain.FeePolicy) error {
	now := time.Now().UTC()
	_, err := s.db.Exec(`INSERT INTO host_fee_policies (host_id, type, value, currency, rounding, min_fee, max_fee, policy_version, updated_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)
		ON CONFLICT(host_id) DO UPDATE SET
			type=excluded.type,
			value=excluded.value,
			currency=excluded.currency,
			rounding=excluded.rounding,
			min_fee=excluded.min_fee,
			max_fee=excluded.max_fee,
			policy_version=excluded.policy_version,
			updated_at=excluded.updated_at;`,
		hostID, policy.Type, policy.Value, policy.Currency, policy.Rounding, policy.MinFee, policy.MaxFee, policy.PolicyVersion, now)
	return err
}

// GetHostPolicy returns host default fee policy.
func (s *SQLiteStore) GetHostPolicy(hostID string) (domain.FeePolicy, error) {
	var policy domain.FeePolicy
	var minFee sql.NullInt64
	var maxFee sql.NullInt64
	if err := s.db.QueryRow(`SELECT type, value, currency, rounding, min_fee, max_fee, policy_version
		FROM host_fee_policies
		WHERE host_id = ?`, hostID).
		Scan(&policy.Type, &policy.Value, &policy.Currency, &policy.Rounding, &minFee, &maxFee, &policy.PolicyVersion); err != nil {
		if err == sql.ErrNoRows {
			return domain.FeePolicy{}, fmt.Errorf("policy not found")
		}
		return domain.FeePolicy{}, err
	}
	if minFee.Valid {
		value := minFee.Int64
		policy.MinFee = &value
	}
	if maxFee.Valid {
		value := maxFee.Int64
		policy.MaxFee = &value
	}
	return policy, nil
}

// UpsertProduct creates or updates product catalog item.
func (s *SQLiteStore) UpsertProduct(product domain.Product) error {
	now := time.Now().UTC()
	active := 0
	if product.IsActive {
		active = 1
	}
	insert := `INSERT INTO products (id, host_id, name, sku, price, is_active, meta, policy_type, policy_value, policy_currency, policy_rounding, policy_min_fee, policy_max_fee, policy_version, created_at, updated_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
		ON CONFLICT(id) DO UPDATE SET
			host_id=excluded.host_id,
			name=excluded.name,
			sku=excluded.sku,
			price=excluded.price,
			is_active=excluded.is_active,
			meta=excluded.meta,
			policy_type=excluded.policy_type,
			policy_value=excluded.policy_value,
			policy_currency=excluded.policy_currency,
			policy_rounding=excluded.policy_rounding,
			policy_min_fee=excluded.policy_min_fee,
			policy_max_fee=excluded.policy_max_fee,
			policy_version=excluded.policy_version,
			updated_at=excluded.updated_at;`
	var pType any
	var pValue any
	var pCurrency any
	var pRounding any
	var pMin any
	var pMax any
	var pVersion any
	if product.FeePolicyOverride != nil {
		pType = string(product.FeePolicyOverride.Type)
		pValue = product.FeePolicyOverride.Value
		pCurrency = product.FeePolicyOverride.Currency
		pRounding = string(product.FeePolicyOverride.Rounding)
		if product.FeePolicyOverride.MinFee != nil {
			pMin = *product.FeePolicyOverride.MinFee
		}
		if product.FeePolicyOverride.MaxFee != nil {
			pMax = *product.FeePolicyOverride.MaxFee
		}
		pVersion = product.FeePolicyOverride.PolicyVersion
	}
	_, err := s.db.Exec(insert, product.ID, product.HostID, product.Name, product.SKU, product.Price, active, jsonStringMap(product.Meta), pType, pValue, pCurrency, pRounding, pMin, pMax, pVersion, now, now)
	return err
}

// GetProduct returns product by id.
func (s *SQLiteStore) GetProduct(productID string) (domain.Product, error) {
	row := s.db.QueryRow(`SELECT id, host_id, name, sku, price, is_active, meta, policy_type, policy_value, policy_currency, policy_rounding, policy_min_fee, policy_max_fee, policy_version
		FROM products
		WHERE id = ?`, productID)
	return scanProduct(row)
}

// ListProducts returns all products.
func (s *SQLiteStore) ListProducts() ([]domain.Product, error) {
	rows, err := s.db.Query(`SELECT id, host_id, name, sku, price, is_active, meta, policy_type, policy_value, policy_currency, policy_rounding, policy_min_fee, policy_max_fee, policy_version
		FROM products ORDER BY id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	items := []domain.Product{}
	for rows.Next() {
		product, scanErr := scanProduct(rows)
		if scanErr != nil {
			return nil, scanErr
		}
		items = append(items, product)
	}
	return items, rows.Err()
}

// UpsertProviderAccount stores host provider credentials.
func (s *SQLiteStore) UpsertProviderAccount(account domain.HostProviderAccount) error {
	now := time.Now().UTC()
	_, err := s.db.Exec(`INSERT INTO host_provider_accounts (host_id, provider, env, credentials_hash, public_config, created_at)
		VALUES (?, ?, ?, ?, ?, ?)
		ON CONFLICT(host_id, provider, env)
		DO UPDATE SET credentials_hash=excluded.credentials_hash, public_config=excluded.public_config;`,
		account.HostID, account.Provider, account.Env, account.CredentialsHash, jsonStringMap(account.PublicConfig), now)
	return err
}

// GetProviderAccounts returns all provider accounts for host.
func (s *SQLiteStore) GetProviderAccounts(hostID string) ([]domain.HostProviderAccount, error) {
	rows, err := s.db.Query(`SELECT host_id, provider, env, credentials_hash, public_config
		FROM host_provider_accounts
		WHERE host_id = ?`, hostID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	accounts := []domain.HostProviderAccount{}
	for rows.Next() {
		var account domain.HostProviderAccount
		var configRaw sql.NullString
		if err := rows.Scan(&account.HostID, &account.Provider, &account.Env, &account.CredentialsHash, &configRaw); err != nil {
			return nil, err
		}
		account.PublicConfig = parseStringMap(configRaw.String)
		accounts = append(accounts, account)
	}
	return accounts, rows.Err()
}

// SaveOrder stores order and ledger snapshot.
func (s *SQLiteStore) SaveOrder(order domain.PaymentOrder, ledger domain.PaymentOrderLedger) error {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	trx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer func() { _ = trx.Rollback() }()

	now := time.Now().UTC()
	if _, err = trx.ExecContext(ctx,
		`INSERT INTO payment_orders (reference, host_id, product_id, provider, provider_reference, provider_checkout_url, currency, env, status, gross_amount, provider_fee_amount, host_fee_amount, net_amount, buyer_ref, policy_snapshot_id, created_at, updated_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
		ON CONFLICT(reference) DO UPDATE SET
			host_id=excluded.host_id,
			product_id=excluded.product_id,
			provider=excluded.provider,
			provider_reference=excluded.provider_reference,
			provider_checkout_url=excluded.provider_checkout_url,
			currency=excluded.currency,
			env=excluded.env,
			status=excluded.status,
			gross_amount=excluded.gross_amount,
			provider_fee_amount=excluded.provider_fee_amount,
			host_fee_amount=excluded.host_fee_amount,
			net_amount=excluded.net_amount,
			buyer_ref=excluded.buyer_ref,
			policy_snapshot_id=excluded.policy_snapshot_id,
			updated_at=excluded.updated_at;`,
		order.Reference, order.HostID, order.ProductID, order.Provider, order.ProviderReference, order.ProviderCheckoutURL,
		order.Currency, order.Env, order.Status, order.GrossAmount, order.ProviderFeeAmount, order.HostFeeAmount, order.NetAmount, order.BuyerRef, order.PolicySnapshotID, now, now); err != nil {
		return err
	}

	var orderID int64
	if err := trx.QueryRowContext(ctx, `SELECT id FROM payment_orders WHERE reference = ?`, order.Reference).Scan(&orderID); err != nil {
		return err
	}

	if _, err := trx.ExecContext(ctx,
		`INSERT INTO payment_order_ledgers (payment_order_id, gross_amount, host_fee_amount, provider_fee_amount, net_amount, policy_checksum, idempotency_key, created_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?)
		ON CONFLICT(idempotency_key) DO UPDATE SET
			gross_amount=excluded.gross_amount,
			host_fee_amount=excluded.host_fee_amount,
			provider_fee_amount=excluded.provider_fee_amount,
			net_amount=excluded.net_amount,
			policy_checksum=excluded.policy_checksum;`,
		orderID, ledger.GrossAmount, ledger.HostFeeAmount, ledger.ProviderFeeAmount, ledger.NetAmount, ledger.PolicyChecksum, ledger.IdempotencyKey, now); err != nil {
		return err
	}
	return trx.Commit()
}

// GetOrder returns order by reference.
func (s *SQLiteStore) GetOrder(reference string) (domain.PaymentOrder, error) {
	return scanOrder(s.db.QueryRow(`SELECT reference, host_id, product_id, provider, provider_reference, provider_checkout_url, currency, env, status, gross_amount, provider_fee_amount, host_fee_amount, net_amount, buyer_ref, policy_snapshot_id
		FROM payment_orders
		WHERE reference = ?`, reference))
}

// GetLedger returns the last ledger for reference.
func (s *SQLiteStore) GetLedger(reference string) (domain.PaymentOrderLedger, error) {
	var ledger domain.PaymentOrderLedger
	if err := s.db.QueryRow(`SELECT l.gross_amount, l.host_fee_amount, l.provider_fee_amount, l.net_amount, l.policy_checksum, l.idempotency_key
		FROM payment_order_ledgers l
		INNER JOIN payment_orders o ON o.id = l.payment_order_id
		WHERE o.reference = ?
		ORDER BY l.id DESC
		LIMIT 1`, reference).
		Scan(&ledger.GrossAmount, &ledger.HostFeeAmount, &ledger.ProviderFeeAmount, &ledger.NetAmount, &ledger.PolicyChecksum, &ledger.IdempotencyKey); err != nil {
		if err == sql.ErrNoRows {
			return domain.PaymentOrderLedger{}, fmt.Errorf("ledger not found")
		}
		return domain.PaymentOrderLedger{}, err
	}
	ledger.PaymentOrderID = reference
	return ledger, nil
}

// ListOrders returns all orders newest first.
func (s *SQLiteStore) ListOrders() ([]domain.PaymentOrder, error) {
	rows, err := s.db.Query(`SELECT reference, host_id, product_id, provider, provider_reference, provider_checkout_url, currency, env, status, gross_amount, provider_fee_amount, host_fee_amount, net_amount, buyer_ref, policy_snapshot_id
		FROM payment_orders
		ORDER BY reference DESC`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	items := []domain.PaymentOrder{}
	for rows.Next() {
		order, scanErr := scanOrder(rows)
		if scanErr != nil {
			return nil, scanErr
		}
		items = append(items, order)
	}
	return items, rows.Err()
}

func scanOrder(scanner interface{ Scan(args ...any) error }) (domain.PaymentOrder, error) {
	var order domain.PaymentOrder
	var status string
	if err := scanner.Scan(
		&order.Reference,
		&order.HostID,
		&order.ProductID,
		&order.Provider,
		&order.ProviderReference,
		&order.ProviderCheckoutURL,
		&order.Currency,
		&order.Env,
		&status,
		&order.GrossAmount,
		&order.ProviderFeeAmount,
		&order.HostFeeAmount,
		&order.NetAmount,
		&order.BuyerRef,
		&order.PolicySnapshotID,
	); err != nil {
		if err == sql.ErrNoRows {
			return domain.PaymentOrder{}, fmt.Errorf("payment not found")
		}
		return domain.PaymentOrder{}, err
	}
	order.ID = order.Reference
	order.Status = domain.PaymentOrderStatus(status)
	return order, nil
}

func scanProduct(scanner interface{ Scan(args ...any) error }) (domain.Product, error) {
	var p domain.Product
	var active int
	var metaRaw sql.NullString
	var pType sql.NullString
	var pValue sql.NullFloat64
	var pCurrency sql.NullString
	var pRounding sql.NullString
	var pMin sql.NullInt64
	var pMax sql.NullInt64
	var pVersion sql.NullString
	if err := scanner.Scan(&p.ID, &p.HostID, &p.Name, &p.SKU, &p.Price, &active, &metaRaw, &pType, &pValue, &pCurrency, &pRounding, &pMin, &pMax, &pVersion); err != nil {
		if err == sql.ErrNoRows {
			return domain.Product{}, fmt.Errorf("product not found")
		}
		return domain.Product{}, err
	}
	p.IsActive = active == 1
	p.Meta = parseStringMap(metaRaw.String)
	if pType.Valid && pCurrency.Valid && pRounding.Valid && pValue.Valid {
		policy := domain.FeePolicy{
			Type:          domain.FeeType(pType.String),
			Value:         pValue.Float64,
			Currency:      pCurrency.String,
			Rounding:      domain.RoundingRule(pRounding.String),
			PolicyVersion: pVersion.String,
		}
		if pMin.Valid {
			minFee := pMin.Int64
			policy.MinFee = &minFee
		}
		if pMax.Valid {
			maxFee := pMax.Int64
			policy.MaxFee = &maxFee
		}
		p.FeePolicyOverride = &policy
	}
	if p.Meta == nil {
		p.Meta = map[string]string{}
	}
	return p, nil
}

func jsonString(values []string) string {
	encoded, _ := json.Marshal(values)
	return string(encoded)
}

func jsonStringMap(values map[string]string) string {
	if len(values) == 0 {
		return "{}"
	}
	encoded, _ := json.Marshal(values)
	return string(encoded)
}

func parseStringSlice(value string) []string {
	if value == "" {
		return []string{}
	}
	vals := []string{}
	_ = json.Unmarshal([]byte(value), &vals)
	return vals
}

func parseStringMap(value string) map[string]string {
	if value == "" {
		return map[string]string{}
	}
	vals := map[string]string{}
	_ = json.Unmarshal([]byte(value), &vals)
	return vals
}

// DeleteHost deletes a host and related dependencies (policy, products, provider accounts) in a transaction.
func (s *SQLiteStore) DeleteHost(hostID string) error {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()

	_, _ = tx.ExecContext(ctx, `DELETE FROM host_provider_accounts WHERE host_id = ?`, hostID)
	_, _ = tx.ExecContext(ctx, `DELETE FROM products WHERE host_id = ?`, hostID)
	_, _ = tx.ExecContext(ctx, `DELETE FROM host_fee_policies WHERE host_id = ?`, hostID)
	
	if _, err := tx.ExecContext(ctx, `DELETE FROM hosts WHERE id = ?`, hostID); err != nil {
		return err
	}
	return tx.Commit()
}

// DeleteProduct deletes a product catalog item.
func (s *SQLiteStore) DeleteProduct(productID string) error {
	_, err := s.db.Exec(`DELETE FROM products WHERE id = ?`, productID)
	return err
}

// DeleteProviderAccount deletes a provider account config.
func (s *SQLiteStore) DeleteProviderAccount(hostID, provider, env string) error {
	_, err := s.db.Exec(`DELETE FROM host_provider_accounts WHERE host_id = ? AND provider = ? AND env = ?`, hostID, provider, env)
	return err
}

