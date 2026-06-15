# Phase Acceptance Criteria

## Phase 0 - Foundation

- [x] `Load()` membaca konfigurasi host, port, environment, timeout.
- [x] Bila env tidak tersedia, default tetap bekerja (`development`, `127.0.0.1:18123`, 10s).
- [x] Pengaturan yang invalid fallback ke nilai default untuk runtime tetap stabil.
- [x] Paket domain dan orchestrator sudah siap dilanjutkan.

## Phase 6 - Self-hosted Foundation

- [x] `POST /register/host` menerima host baru dan terbaca saat lookup/list.
- [x] `POST /register/product` menerima produk aktif/nonaktif + fee policy override.
- [x] `POST /register/provider-account` menerima mapping credential per env.
- [x] `POST /register/host-policy` menyimpan policy default host.
- [x] Data host/product/order tersimpan ke SQLite lewat `HOST_RUTEBAYAR_DATABASE_DSN`.
- [x] Dashboard `/ui` menampilkan daftar host, produk, dan order.

## Phase 7 - Host-scoped Integration

- [x] Jika `HOST_RUTEBAYAR_UPSTREAM_BASE_URL` diset, `/host/{host_id}/payments` dan `/host/{host_id}/payments/{reference}` diproxy ke upstream.
- [x] Proxy menyertakan header `X-Host-ID`.
- [x] Jalur host-scoped mengikuti contract yang dipetakan di OpenAPI.

## Phase 8 - Operasional dan Acceptance

- [x] Self-hosted runbook tercatat lengkap (`docs/runbook.md`).
- [x] Contoh alur callback + enkripsi/discrepancy check tercatat untuk onboarding host (`docs/callback-and-observability.md`).
- [x] Semua endpoint/fitur penting terdokumentasi di `internal/api/openapi.yaml`.
- [x] Acceptance criteria per-fase terpusat di file ini.

## Ringkasan risiko operasional

- Callback endpoint harus dibatasi allowlist dan secret per host.
- Webhook dan callback harus diberi idempotency key + signature.
- Jalur observability (metric, audit, DLQ) perlu dipantau secara berkala saat trafik naik.
