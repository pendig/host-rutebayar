# host-rutebayar

host-rutebayar adalah layanan marketplace/payment-orchestrator yang memisahkan peran platform host UMKM dari layer gateway murni yang ada di `rute-bayar`.

## Tujuan

Repo ini ditujukan untuk membuat model seperti ini:

- Host mendaftar dan membuat produk yang dijual.
- Host menentukan produk yang aktif.
- Host mengatur environment `sandbox` dan `production`.
- Saat ada pembayaran, host-bayar/website membuat transaksi melalui host-rutebayar.
- host-rutebayar menyimpan mapping pesanan dan membangun request ke gateway.
- Data pembeli (buyer) tetap dienkripsi dan tidak dipakai untuk kebutuhan internal gateway langsung.
- Webhook dari gateway diverifikasi, lalu diteruskan terenkripsi ke website host sesuai produk.

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

## Konsep environment

- `sandbox`: dipakai untuk testing host + integration.
- `production`: environment sesungguhnya, terpisah key/credential dan base URL.

## Security checklist awal

- `host_secret` dan `webhook_secret` wajib unik per-host.
- Data pembeli dienkripsi-at-rest.
- Semua webhook diverifikasi signature/hmac provider terlebih dahulu.
- Enkripsi payload callback ke website host (atau signed payload + expiry).
- Idempotency untuk webhook (mencegah double-processing).
- Audit log untuk create/payment/webhook events.

## Rencana kerja

Lihat `PLAN.md`.

## Status

- Inisialisasi repo dan dokumentasi ide + rencana.
- Menunggu review PR sebelum implementasi lanjut.
