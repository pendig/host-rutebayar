package domain

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"math"
)

// FeeType enumerates policy fee strategies.
type FeeType string

const (
	FeeTypePercent FeeType = "percent"
	FeeTypeFixed   FeeType = "fixed"
	FeeTypeFree    FeeType = "free"
)

// RoundingRule describes how percentage fee is rounded.
type RoundingRule string

const (
	RoundingRuleNearest RoundingRule = "nearest"
	RoundingRuleFloor   RoundingRule = "floor"
	RoundingRuleCeil    RoundingRule = "ceil"
)

// Host captures platform host profile metadata.
type Host struct {
	ID                string
	Name              string
	CallbackURLs      []string
	CallbackAllowlist []string
	NotificationKey   string
	HostSecret        string
	WebhookSecret     string
}

func (h Host) Validate() error {
	if h.ID == "" {
		return errors.New("host id is required")
	}
	if h.Name == "" {
		return errors.New("host name is required")
	}
	if h.NotificationKey == "" || h.HostSecret == "" || h.WebhookSecret == "" {
		return errors.New("host secrets are required")
	}
	return nil
}

// FeePolicy defines default or override fee behavior.
type FeePolicy struct {
	Type          FeeType
	Value         float64
	Currency      string
	Rounding      RoundingRule
	MinFee        *int64
	MaxFee        *int64
	PolicyVersion string
}

func (p FeePolicy) Validate() error {
	if p.Currency == "" {
		return errors.New("currency is required")
	}
	if p.Value < 0 {
		return errors.New("fee value cannot be negative")
	}
	switch p.Type {
	case FeeTypePercent:
		if p.Value > 100 {
			return fmt.Errorf("percent fee cannot exceed 100, got %.6f", p.Value)
		}
	case FeeTypeFixed:
		if p.Value != math.Trunc(p.Value) {
			return errors.New("fixed fee must be integer value")
		}
	case FeeTypeFree:
		if p.Value != 0 {
			return errors.New("free fee value must be 0")
		}
	default:
		return errors.New("unsupported fee type")
	}
	switch p.Rounding {
	case RoundingRuleNearest, RoundingRuleFloor, RoundingRuleCeil, "":
	default:
		return errors.New("unsupported rounding rule")
	}
	if p.MinFee != nil && p.MaxFee != nil && *p.MinFee > *p.MaxFee {
		return errors.New("min fee cannot exceed max fee")
	}
	return nil
}

func (p FeePolicy) roundAmount(raw float64) int64 {
	switch p.Rounding {
	case RoundingRuleCeil:
		return int64(math.Ceil(raw))
	case RoundingRuleFloor:
		return int64(math.Floor(raw))
	default:
		return int64(math.Round(raw))
	}
}

// CalculateHostFee returns host fee for gross amount and applies optional min/max clamps.
func (p FeePolicy) CalculateHostFee(gross int64) (int64, error) {
	if err := p.Validate(); err != nil {
		return 0, err
	}
	if gross <= 0 {
		return 0, nil
	}

	var fee float64
	switch p.Type {
	case FeeTypePercent:
		fee = float64(gross) * (p.Value / 100)
	case FeeTypeFixed:
		fee = p.Value
	case FeeTypeFree:
		return 0, nil
	}

	amt := p.roundAmount(fee)
	if p.MinFee != nil && amt < *p.MinFee {
		amt = *p.MinFee
	}
	if p.MaxFee != nil && amt > *p.MaxFee {
		amt = *p.MaxFee
	}
	return amt, nil
}

// FeePolicySnapshot stores immutable policy version at order creation.
type FeePolicySnapshot struct {
	PolicyID          string
	PolicyVersion     string
	PolicyPayloadHash string
}

func NewFeePolicySnapshot(policy FeePolicy) FeePolicySnapshot {
	bytes, _ := json.Marshal(policy)
	hash := sha256.Sum256(bytes)
	return FeePolicySnapshot{
		PolicyID:          fmt.Sprintf("policy-%s", policy.PolicyVersion),
		PolicyVersion:     policy.PolicyVersion,
		PolicyPayloadHash: hex.EncodeToString(hash[:]),
	}
}

// Product owned by host.
type Product struct {
	ID                string
	HostID            string
	Name              string
	SKU               string
	Price             int64
	IsActive          bool
	Meta              map[string]string
	FeePolicyOverride *FeePolicy
}

func (p Product) Validate() error {
	if p.ID == "" {
		return errors.New("product id is required")
	}
	if p.HostID == "" {
		return errors.New("host id is required")
	}
	if p.Name == "" {
		return errors.New("product name is required")
	}
	if p.Price < 0 {
		return errors.New("product price cannot be negative")
	}
	if p.FeePolicyOverride != nil {
		if err := p.FeePolicyOverride.Validate(); err != nil {
			return fmt.Errorf("product fee override invalid: %w", err)
		}
	}
	return nil
}

// HostProviderAccount stores per-host gateway credentials.
type HostProviderAccount struct {
	HostID          string
	Provider        string
	Env             string
	CredentialsHash string
	PublicConfig    map[string]string
}

func (a HostProviderAccount) Validate() error {
	if a.HostID == "" {
		return errors.New("host id is required")
	}
	if a.Provider == "" {
		return errors.New("provider is required")
	}
	if a.Env == "" {
		return errors.New("environment is required")
	}
	if a.CredentialsHash == "" {
		return errors.New("credentials hash is required")
	}
	return nil
}

// PaymentOrderStatus shows payment lifecycle state.
type PaymentOrderStatus string

const (
	PaymentOrderStatusCreated PaymentOrderStatus = "created"
	PaymentOrderStatusSuccess PaymentOrderStatus = "success"
	PaymentOrderStatusFailed  PaymentOrderStatus = "failed"
	PaymentOrderStatusExpired PaymentOrderStatus = "expired"
)

// PaymentOrder stores immutable transaction snapshot and calc output.
type PaymentOrder struct {
	ID                  string
	Reference           string
	HostID              string
	ProductID           string
	Provider            string
	Currency            string
	Env                 string
	Status              PaymentOrderStatus
	GrossAmount         int64
	HostFeeAmount       int64
	ProviderFeeAmount   int64
	NetAmount           int64
	BuyerRef            string
	PolicySnapshotID    string
	ProviderReference   string
	ProviderCheckoutURL string
}

// PaymentOrderLedger stores immutable settlement line.
type PaymentOrderLedger struct {
	PaymentOrderID    string
	GrossAmount       int64
	HostFeeAmount     int64
	ProviderFeeAmount int64
	NetAmount         int64
	PolicyChecksum    string
	IdempotencyKey    string
}

// WebhookRoute stores mapping + retry strategy for callback host.
type WebhookRoute struct {
	ID             string
	HostID         string
	PaymentOrderID string
	CallbackURL    string
	RetryAttempts  int
	RetryBackoffMs int
}
