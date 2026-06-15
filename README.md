# host-rutebayar

host-rutebayar adalah layanan marketplace/payment-orchestrator yang memisahkan peran platform host UMKM dari layer gateway murni yang ada di `rute-bayar`.

## Tujuan

Repo ini ditujukan untuk membuat model seperti ini:

- Host mendaftar dan membuat produk yang dijual.
- Host menentukan produk yang aktif.
- Host menentukan kebijakan fee untuk transaksi (persen/fixed) per produk; termasuk boleh `0` (gratis).
- Host mengatur environment `sandbox` dan `production`.
- Saat ada pembayaran, host-bayar/website membuat transaksi melalui host-rutebayar.
- host-rutebayar menyimpan mapping pesanan dan membangun request ke gateway.
- Data pembeli (buyer) tetap dienkripsi dan tidak dipakai untuk kebutuhan internal gateway langsung.
- Webhook dari gateway diverifikasi, lalu diteruskan terenkripsi ke website host sesuai produk.

## Model fee host

- Fee bisa diatur per-host sebagai default policy.
- Host juga bisa override fee per-produk.
- Tipe fee:
  - `percent` (contoh: `2.5%`)
  - `fixed` (contoh: `Rp 1.000`)
  - `free` (`0`, tidak dipungut fee)
- Fee diproses dari nominal pembayaran sesuai rounding policy yang didokumentasikan (mis. 0 desimal untuk IDR).
- Policy bisa pakai `min_fee`, `max_fee`, dan `currency`; dan boleh dipakai sebagai
  `free` untuk produk tertentu.
- Fee bukan domain `rute-bayar`; host-rutebayar sebagai orchestrator menentukan net amount yang diteruskan ke host website/merchant.

## Fee policy snapshot & settlement

- Saat payment dibuat, host-rutebayar mengambil snapshot `FeePolicySnapshot` (mis. `policy_snapshot_id`, `policy_version`, `policy_payload_hash`).
- Snapshot disimpan di `PaymentOrder` agar policy fee tidak berubah saat transaksi berlangsung.
- Hasil kalkulasi (gross/host fee/provider fee/net) tetap immutable di `PaymentOrder` untuk kebutuhan audit.
- Data settlement diekspor sebagai ledger line:
  - `gross_amount`
  - `provider_fee_amount`
  - `host_fee_amount`
  - `net_amount`

## Reuse OpenAPI rute-bayar untuk MVP cepat

- Repo `rute-bayar` sudah punya kontrak API yang bisa langsung dipakai:
  - `internal/api/openapi.yaml`
  - `docs/api-spec.md` (ringkasan endpoint)
- Untuk MVP, host-bayar bisa menjadi layer orchestrator, bukan builder adapter provider:
  - maintain policy+tenant di `host-rutebayar`,
  - forward request invoice/payment ke endpoint daemon rute-bayar (`POST /api/v1/payments`, `GET /api/v1/payments/{reference}`, `GET /api/v1/payments/{reference}/status`),
  - biarkan daemon rute-bayar menerima/verifikasi webhook dan `Forwarding` event,
  - host-bayar menambahkan enrich (policy/fee) lalu push callback terenkripsi ke website host.
- Pendekatan ini cocok untuk MVP karena rute-bayar sudah punya endpoint dasar + proof create->webhook->reconcile.

## Scope awal (v0)

- Repository ini difokuskan untuk desain data & API agar review ide sebelum implementasi penuh.
- Bukan implementasi gateway baru—gateway tetap di handle di layer `rute-bayar` (existing).
- Fokus awal: model tenant/produk/produk-aktif-host + orchestrator alur pembayaran + event router webhook.

## Nilai tambah dibandingkan langsung pakai gateway

- Host bisa punya banyak produk + aturan sendiri.
- Onboarding gateway per-host dapat dikelola secara independen.
- Integrasi website host lebih konsisten karena cukup pakai SDK/endpoint dari host-rutebayar.
- Audit + routing webhook lebih terpusat.

## Alur kasar

1. Host/website mendaftarkan host profile + product catalog.
2. Host memanggil API host-rutebayar untuk membuat invoice/payment request.
3. host-rutebayar membuat intent internal + memilih provider sesuai policy host/product.
4. host-rutebayar memanggil provider (via wrapper adapter) untuk mendapatkan URL/payment reference.
5. Webhook provider masuk ke host-rutebayar, diverifikasi, lalu dibaca dari mapping.
6. host-rutebayar mengirimkan event terenkripsi ke endpoint callback website host.
7. host-rutebayar memproses fee sesuai policy host/product, lalu event final disertakan detail komisi dan gross/net amount.

## Konsep environment

- `sandbox`: dipakai untuk testing host + integration.
- `production`: environment sesungguhnya, terpisah key/credential dan base URL.

## Konfigurasi runtime

- `HOST_RUTEBAYAR_ENV` (default `development`)
- `HOST_RUTEBAYAR_HOST` (default `127.0.0.1`)
- `HOST_RUTEBAYAR_PORT` (default `8080`)
- `HOST_RUTEBAYAR_TIMEOUT` (default `10s`)
- `HOST_RUTEBAYAR_DATABASE_DSN` (default `file:host-rutebayar.db?_pragma=foreign_keys(ON)`)
- `HOST_RUTEBAYAR_UPSTREAM_BASE_URL` (opsional) — saat diisi, path `/host/{id}/payments...` akan diproksi ke upstream rute-bayar (`/api/v1/...`).

## Security checklist awal

- `host_secret` dan `webhook_secret` wajib unik per-host.
- Data pembeli dienkripsi-at-rest.
- Semua webhook diverifikasi signature/hmac provider terlebih dahulu.
- Enkripsi payload callback ke website host (atau signed payload + expiry).
- Callback host dibatasi ke allowlist endpoint & signature HMAC per-host + timestamp/nonce.
- Idempotency untuk webhook (mencegah double-processing).
- Audit log untuk create/payment/webhook events.
- Fee policy diverifikasi dan ditandatangani agar tidak bisa diubah di sisi client selama transaksinya.

## Integrasi dan paket SDK

- `host-rutebayar`:
  - menyediakan endpoint API publik `host-rutebayar` untuk registrasi host, produk, pembayaran.
  - menyediakan SDK (opsional versi awal) untuk integrasi website host.
- `rute-bayar` tetap memegang adapter provider (Xendit/Midtrans/Doku/IPaymu).
- Callback host dan signature logic dikelola agar website host tidak perlu menyentuh detail gateway.

## Rencana kerja

Lihat `PLAN.md`.

## Status

- Inisialisasi repo dan dokumentasi ide + rencana.
- Menunggu review PR sebelum implementasi lanjut.
