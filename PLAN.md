# Plan: host-rutebayar (Initial)

## Phase 0 - Foundation (Dokumentasi + Contract)

- [x] Inisialisasi repository `host-rutebayar` di GitHub org `pendig` (public).
- [x] Menyusun proposal awal di `README.md`.
- [ ] Finalisasi kebutuhan MVP dan acceptance criteria.
- [ ] Menetapkan schema API public (`/v1`) + payload event.
- [ ] Menetapkan batas tanggung jawab: `host-rutebayar` vs `rute-bayar`.

## Phase 1 - Core Domain

- [ ] `Host`:
  - id, name, callback_urls, callback_allowlist, notification_key, host_secret, webhook_secret.
- [ ] `FeePolicy`:
  - scope: host default (`host_id`) + optional product override.
  - fields: type (`percent|fixed|free`), value, currency, rounding_rule, min_fee, max_fee, effective_from/until, policy_version.
- [ ] `FeePolicySnapshot`:
  - policy_id, policy_payload_hash, policy_version, effective_from, effective_until.
- [ ] `Product`:
  - id, host_id, name, sku, price, is_active, metadata.
  - support `fee_policy_override` (opsional) untuk override default host.
- [ ] `HostProviderAccount`:
  - per-host credentials map (xendit/midtrans/doku/ipaymu), env sandbox/prod.
- [ ] `PaymentOrder`:
  - host_id, product_id, provider, currency, env, reference, status, gross_amount, host_fee_amount, provider_fee_amount, net_amount, buyer_ref, policy_snapshot_id.
- [ ] `PaymentOrderLedger`:
  - payment_order_id, gross_amount, host_fee_amount, provider_fee_amount, net_amount, policy_checksum, idempotency_key.
- [ ] `WebhookRoute`:
  - event mapping + retry policy ke host endpoint.

## Phase 2 - Orchestration Engine

- [ ] SDK/helper untuk website host membuat order.
- [ ] Kalkulasi fee saat pembuatan order:
  - Terapkan fee berdasarkan `Product` override jika ada, fallback `Host` default.
  - Validasi nilai fee (`percent` 0-100, `fixed` >= 0, `free` = 0, cap/min valid).
  - Simpan snapshot policy + hasil `host_fee_amount` + `provider_fee_amount` + `net_amount` ke `PaymentOrder`.
- [ ] Endpoint `POST /payments` membuat internal order + pilih provider.
- [ ] Endpoint `GET /payments/{ref}` untuk status.
- [ ] Endpoint callback untuk webhook provider:
  - verify signature
  - parse event
  - parse idempotency key
  - reconcile status
  - push event terenkripsi ke callback host.
  - kirim detail gross/fee/net + checksum policy version untuk audit.
  - tulis `PaymentOrderLedger` + kirim `policy_version`.
- [ ] Tambahkan retry callback host:
  - exponential backoff
  - dead-letter queue (DLQ)
  - alert threshold untuk repeat fail.

## Phase 3 - Security-first hardening

- [ ] Atur kunci enkripsi + KMS integration (AWS/GCP/KMS sesuai deployment).
- [ ] Key rotation + rotate endpoint.
- [ ] Token/secret scoping per host.
- [ ] Secret versioning dengan 2 secret key aktif saat rollover.
- [ ] Rate-limit + replay protection + anti-brute force.
- [ ] Sensitive masking di logs.

## Phase 4 - Observability + Operasional

- [ ] Audit trail immutable.
- [ ] Monitoring webhook failure/delivery.
- [ ] Monitoring metrik per-host: success rate, fee mismatch, webhook lag.
- [ ] Dead letter/retry untuk callback host.
- [ ] Health/readiness & runbooks.

## Phase 5 - Implementation split

- [ ] Decide package boundary:
  - `host-rutebayar` = orchestrator & registry.
  - `rute-bayar` = adapter/payment-router/utility.
- [ ] Implement SDK client library (minimal API wrapper).
- [ ] Implement OpenAPI proxy layer in Phase 5 using `rute-bayar` contract:
  - source of truth: `internal/api/openapi.yaml`.
  - forward create flow `/host/{id}/payments` -> `rute-bayar /api/v1/payments`.
  - forward status flow `/host/{id}/payments/{reference}` -> `rute-bayar /api/v1/payments/{reference}`.
  - replay webhook event dari rute-bayar, enrich host detail, lalu fanout ke callback host.
- [ ] Canary + sandbox test + dokumentasi onboarding host.

## Phase 6 - Self-hosted Foundation (Registry & Persistence)

- [x] Implementasi registrasi host/product/provider-policy melalui handler yang tersimpan di SQLite.
- [x] Integrasi handler orchestrator dengan storage sqlite untuk data host/prod/akun/provider/order.
- [x] Implementasi koneksi gateway adapter + event lifecycle minimal.
- [x] Dashboard self-hosted sederhana menampilkan daftar host, produk, dan order.

## Phase 7 - Host-scoped API Integration

- [x] Tambah route `HOST_RUTEBAYAR_UPSTREAM_BASE_URL` agar `/host/{host_id}/...` bisa diproksi ke kontrak rute-bayar.
- [x] Routing runtime otomatis mount prefix `/host/` ke `internal/proxy` bila upstream diset.
- [x] Update OpenAPI agar mencakup endpoint `register` + host-scoped path.

## Phase 8 - Operasional dan Acceptance

- [x] Lengkapi runbook self-hosted (env, migrate, start, cek kesehatan).
- [x] Tambahkan contoh integrasi callback/enkripsi + observability checklist.
- [x] Pastikan acceptance criteria per-fase sudah terdokumentasi di satu file.
