# Plan: host-rutebayar (Initial)

## Phase 0 - Foundation (Dokumentasi + Contract)

- [x] Inisialisasi repository `host-rutebayar` di GitHub org `pendig` (public).
- [x] Menyusun proposal awal di `README.md`.
- [ ] Finalisasi kebutuhan MVP dan acceptance criteria.
- [ ] Menetapkan schema API public (`/v1`) + payload event.
- [ ] Menetapkan batas tanggung jawab: `host-rutebayar` vs `rute-bayar`.

## Phase 1 - Core Domain

- [ ] `Host`:
  - id, name, callback_url, notification_key.
- [ ] `FeePolicy`:
  - scope: host default (`host_id`) + optional product override.
  - fields: type (`percent|fixed|free`), value, currency, rounding_rule, effective_from/until.
- [ ] `Product`:
  - id, host_id, name, sku, price, is_active, metadata.
  - support `fee_policy_override` (opsional) untuk override default host.
- [ ] `HostProviderAccount`:
  - per-host credentials map (xendit/midtrans/doku/ipaymu), env sandbox/prod.
- [ ] `PaymentOrder`:
  - host_id, product_id, provider, env, reference, status, amount, fee_amount, net_amount, buyer_ref.
- [ ] `WebhookRoute`:
  - event mapping + retry policy ke host endpoint.

## Phase 2 - Orchestration Engine

- [ ] SDK/helper untuk website host membuat order.
- [ ] Kalkulasi fee saat pembuatan order:
  - Terapkan fee berdasarkan `Product` override jika ada, fallback `Host` default.
  - Validasi nilai fee (`percent` > 0, `free` = 0, fixed tidak negatif, optional cap/min).
  - Simpan `fee_amount` dan `net_amount` ke `PaymentOrder`.
- [ ] Endpoint `POST /payments` membuat internal order + pilih provider.
- [ ] Endpoint `GET /payments/{ref}` untuk status.
- [ ] Endpoint callback untuk webhook provider:
  - verify signature
  - parse event
  - reconcile status
  - push event terenkripsi ke callback host.
  - kirim detail gross/fee/net + checksum policy version untuk audit.

## Phase 3 - Security-first hardening

- [ ] Atur kunci enkripsi + KMS integration (AWS/GCP/KMS sesuai deployment).
- [ ] Key rotation + rotate endpoint.
- [ ] Token/secret scoping per host.
- [ ] Rate-limit + replay protection + anti-brute force.
- [ ] Sensitive masking di logs.

## Phase 4 - Observability + Operasional

- [ ] Audit trail immutable.
- [ ] Monitoring webhook failure/delivery.
- [ ] Dead letter/retry untuk callback host.
- [ ] Health/readiness & runbooks.

## Phase 5 - Implementation split

- [ ] Decide package boundary:
  - `host-rutebayar` = orchestrator & registry.
  - `rute-bayar` = adapter/payment-router/utility.
- [ ] Implement SDK client library (minimal API wrapper).
- [ ] Canary + sandbox test + dokumentasi onboarding host.
