# Self-hosted Runbook

## 1. Prasyarat

- Go 1.25.11+ pada mesin operasi.
- Direktori kerja yang dapat menulis file `host-rutebayar.db`.
- Kunci kredensial `host_secret` dan `webhook_secret` siap per-host.
- (Opsional) upstream rute-bayar bila pakai mode proxy host-scoped.

## 2. Konfigurasi minimum

```bash
export HOST_RUTEBAYAR_ENV=development
export HOST_RUTEBAYAR_HOST=127.0.0.1
export HOST_RUTEBAYAR_PORT=18123
export HOST_RUTEBAYAR_TIMEOUT=10s
export HOST_RUTEBAYAR_DATABASE_DSN='file:host-rutebayar.db?_pragma=foreign_keys(ON)'
# Password dashboard admin (opsional untuk local dev; default: admin123)
# export HOST_RUTEBAYAR_ADMIN_PASSWORD='change-me'
# Opsional
# export HOST_RUTEBAYAR_UPSTREAM_BASE_URL=http://127.0.0.1:8080
```

## 3. Jalankan service

```bash
go run ./cmd/host-rutebayar
```

Akses:
- Health check: `curl http://127.0.0.1:18123/health`
- Dashboard: `http://127.0.0.1:18123/ui` (akan redirect ke `http://127.0.0.1:18123/ui/login`)

## 4. Inisialisasi data (minimal)

Untuk bootstrap data demo dari CLI, login dulu lewat dashboard atau endpoint UI login supaya session admin tersedia.

```bash
curl -sS -c /tmp/hrb.session -X POST http://127.0.0.1:18123/ui/login \
  --data "password=admin123&next=%2Fui"

curl -sS -b /tmp/hrb.session -X POST http://127.0.0.1:18123/admin/demo-seed \
  -H "Content-Type: application/json" \
  -d '{}'
```

Catatan: ganti password admin bila diperlukan (lihat bagian [8. Login dashboard](#8-login-dashboard)).

Atau buat manual:

```bash
curl -X POST http://127.0.0.1:18123/register/host \
  -H "Content-Type: application/json" \
  -d '{"id":"host-demo","name":"Demo Host","callback_urls":["https://example.com/callback"],"callback_allowlist":["https://example.com"],"notification_key":"dev-notif-key","host_secret":"host_secret_123","webhook_secret":"webhook_secret_123"}'

curl -X POST http://127.0.0.1:18123/register/product \
  -H "Content-Type: application/json" \
  -H "X-Host-Secret: host_secret_123" \
  -d '{"id":"prod-001","host_id":"host-demo","name":"Paket Demo","sku":"PKT-001","price":120000,"is_active":true}'

curl -X POST http://127.0.0.1:18123/register/provider-account \
  -H "Content-Type: application/json" \
  -H "X-Host-Secret: host_secret_123" \
  -d '{"host_id":"host-demo","provider":"midtrans","env":"sandbox","credentials_hash":"sha256:...","public_config":{"merchant_id":"m-dummy"}}'
```

## 5. Uji alur pembayaran

```bash
curl -X POST http://127.0.0.1:18123/payments \
  -H "Content-Type: application/json" \
  -H "X-Host-Secret: host_secret_123" \
  -d '{"host_id":"host-demo","product_id":"prod-001","env":"sandbox","buyer_ref":"order-001"}'
```

Lalu cek status dengan `reference` dari response.

## 6. Operasional harian

- Pantau dashboard operasional di `http://127.0.0.1:18123/ui`.
- Pantau callback monitor/retry di `http://127.0.0.1:18123/ui/callbacks`.
- Pantau `go test ./...` di setiap perubahan.
- Backup DB secara berkala:
  - `sqlite3 host-rutebayar.db ".backup 'host-rutebayar.db.$(date +%F_%H%M%S)'"`
  - atau `cp` hanya aman saat proses benar-benar tidak menulis.
- Restart service saat mengubah credential policy/akun host.
- Verifikasi webhook callback endpoint aktif dari monitor uptime host.
- Jika proxy aktif, verifikasi request host-scoped tetap mendapatkan `200/202` dari upstream.

## 7. Rollback cepat

- Hentikan proses service.
- Pulihkan `host-rutebayar.db` dari backup ke lokasi file data (contoh `cp host-rutebayar.db.2026-06-15_123000 host-rutebayar.db`).
- Tetapkan `HOST_RUTEBAYAR_DATABASE_DSN` tetap menunjuk ke `host-rutebayar.db`.
- Restart service dan verifikasi `/health`.

## 8. Login dashboard

- Dashboard admin di `/ui` dilindungi session cookie.
- Tetapkan password lewat env:
  ```bash
  export HOST_RUTEBAYAR_ADMIN_PASSWORD='ganti-password'
  ```
- Jika lupa password, atur ulang env lalu restart service.
