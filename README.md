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
- Data pembeli (buyer) hanya digunakan untuk kebutuhan alur pembayaran dan pencatatan internal order.
- Webhook dari gateway diverifikasi, lalu direkonsiliasi ke status order internal.
- Pengiriman callback ke website host dari service ini belum disertakan pada fase ini; alur fanout host ditangani pada fase lanjutan.

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

## Kontrak API dan integrasi

Kontrak API operasional service ini berada di:
- `internal/api/openapi.yaml`

Integrasi dengan `rute-bayar` saat ini:
- `HOST_RUTEBAYAR_UPSTREAM_BASE_URL` dapat dipakai untuk route `host-scoped` ke daemon rute-bayar.
- `POST /host/{host_id}/payments` dan `GET /host/{host_id}/payments/{reference}` diproksi ke endpoint upstream `rute-bayar` bila dikonfigurasi.
- Rekonsiliasi webhook dilakukan di `host-rutebayar` untuk menjaga state order dan ledger.

## Scope saat ini (fase 6-8)

- Repository ini fokus pada orchestrasi host+produk+policy, lifecycle pembayaran, dan operasional self-hosted.
- Bukan implementasi gateway baru—gateway tetap di handle di layer `rute-bayar` (existing).
- Fokus saat ini: registrasi tenant/product/provider, pembuatan payment, webhook reconcile, dan dashboard observability.

## Nilai tambah dibandingkan langsung pakai gateway

- Host bisa punya banyak produk + aturan sendiri.
- Onboarding gateway per-host dapat dikelola secara independen.
- Integrasi website host lebih konsisten karena bisa memakai endpoint host-rutebayar yang terstandar.
- Audit + routing webhook lebih terpusat.

## Alur kasar

1. Host/website mendaftarkan host profile + product catalog.
2. Host memanggil API host-rutebayar untuk membuat invoice/payment request.
3. host-rutebayar membuat intent internal + memilih provider sesuai policy host/product.
4. host-rutebayar memanggil provider (via wrapper adapter) untuk mendapatkan URL/payment reference.
5. Webhook provider masuk ke host-rutebayar, diverifikasi, lalu dibaca dari mapping.
6. host-rutebayar menyimpan status final + ledger untuk observability operasional.
7. host-rutebayar memproses fee sesuai policy host/product, lalu event final tersimpan bersama detail komisi dan gross/net amount.

## Konsep environment

- `sandbox`: dipakai untuk testing host + integration.
- `production`: environment sesungguhnya, terpisah key/credential dan base URL.

## Login dashboard

Dashboard operasi di `/ui` akan mengarahkan ke `/ui/login` jika belum autentikasi.
Default password login fallback `admin123` untuk local development, tetapi untuk non-local/prod harus diganti lewat:

- `HOST_RUTEBAYAR_ADMIN_PASSWORD`

Catatan keamanan: jangan pernah hardcode password ini ke source/docs publik.

## Konfigurasi runtime

- `HOST_RUTEBAYAR_ENV` (default `development`)
- `HOST_RUTEBAYAR_HOST` (default `127.0.0.1`)
- `HOST_RUTEBAYAR_PORT` (default `18123`)
- `HOST_RUTEBAYAR_TIMEOUT` (default `10s`)
- `HOST_RUTEBAYAR_DATABASE_DSN` (default `file:host-rutebayar.db?_pragma=foreign_keys(ON)`)
- `HOST_RUTEBAYAR_UPSTREAM_BASE_URL` (opsional) — saat diisi, path `/host/{id}/payments...` akan diproksi ke upstream rute-bayar (`/api/v1/...`).
- `HOST_RUTEBAYAR_ADMIN_PASSWORD` — password login untuk dashboard (opsional di local dev, wajib di non-local/prod).

## Security checklist fase 6-8

- `host_secret` dan `webhook_secret` wajib unik per-host.
- Semua webhook diverifikasi signature/idempotency terlebih dahulu.
- Rekonsiliasi webhook menghasilkan status/order/ledger yang terukur.
- Idempotency untuk webhook untuk mencegah double-processing.
- Audit log untuk create/payment/webhook events.
- Fee policy dipertahankan konsisten selama transaksi untuk mencegah drift perhitungan.

## Integrasi dan paket SDK

- `host-rutebayar`:
  - menyediakan endpoint API untuk registrasi host, produk, policy, dan pembayaran.
  - menyediakan dashboard self-hosted `/ui` untuk monitoring operasional.
- `rute-bayar` tetap memegang adapter provider (Xendit/Midtrans/Doku/IPaymu).
- Callback host outbound dan pengiriman ke URL host belum aktif pada fase ini.

## Struktur Direktori

Berikut adalah struktur utama dari codebase project ini:

* `cmd/host-rutebayar/`: Entry point utama aplikasi.
* `api/openapi.yaml`: Kontrak spesifikasi API OpenAPI.
* `docs/`: Dokumentasi teknis, runbook operasional, dan kriteria penerimaan.
* `internal/`: Logika internal aplikasi (tidak dapat di-import oleh package luar).
  * `api/`: REST API Controllers/Handlers (registrasi host, produk, provider, dll.).
  * `config/`: Parser dan validator konfigurasi runtime.
  * `domain/`: Model data/entitas inti (`Host`, `Product`, `PaymentOrder`, dll.).
  * `gateway/`: Abstraksi provider gateway.
  * `http/`: Router HTTP, middleware, UI dashboard admin, dan static assets.
  * `observability/`: Logging, metrik audit trail, dan alert thresholds.
  * `orchestration/`: Kalkulasi fee, intent matching, settlement, dan ledger.
  * `proxy/`: Proxy layer untuk mem-forward request host-scoped ke upstream `rute-bayar`.
  * `security/`: Verifikasi signature webhook, kriptografi kunci, dan otentikasi dashboard.
  * `storage/`: Database access layer SQLite dan migrasi schema.

## Memulai Pengembangan (Getting Started)

### Prasyarat
- Go 1.23+
- SQLite3 (driver database tertanam otomatis)

### Menjalankan Unit Test
Semua kontribusi kode wajib lolos pengujian unit test. Jalankan test suite dengan perintah berikut:
```bash
go test -v ./...
```

### Menjalankan Aplikasi Secara Lokal
1. Build binary aplikasi:
   ```bash
   go build -o host-rutebayar ./cmd/host-rutebayar
   ```
2. Jalankan binary dengan konfigurasi default:
   ```bash
   ./host-rutebayar
   ```
3. Akses dashboard di browser pada `http://127.0.0.1:18123/ui` (password login default: `admin123`).

Untuk petunjuk operasional dan skrip seeding lengkap, silakan merujuk ke [Self-hosted Runbook](docs/runbook.md).

## Rencana Kerja & Status

- Detail peta jalan proyek dapat dilihat di [PLAN.md](PLAN.md).
- Status saat ini: Fase 6-8 telah selesai diimplementasikan (registrasi host/product/provider, payment flow, webhook reconcile, dashboard, dan runbook).
- Operasional lanjutan difokuskan ke hardening dan callback fanout host outbound.

## Kontribusi & Lisensi

- Kontribusi baru sangat kami hargai. Silakan baca [CONTRIBUTING.md](CONTRIBUTING.md) sebelum mengirimkan Pull Request.
- Project ini dilisensikan di bawah [MIT License](LICENSE).

