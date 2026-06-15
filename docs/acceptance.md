# Fase 0 Acceptance Criteria

- `Load()` membaca konfigurasi host, port, environment, timeout.
- Bila env tidak tersedia, default tetap bekerja (`development`, `127.0.0.1:18123`, 10s).
- Pengaturan yang invalid akan diabaikan dengan fallback default agar runtime tetap stabil.
- Struktur paket siap untuk implementasi domain dan orchestrator pada fase berikutnya.

# Fase 6 Acceptance Criteria

- `POST /register/host` menerima host baru dan dapat dibaca saat list/lookup.
- `POST /register/product` menerima produk aktif/nonaktif, termasuk override policy fee.
- `POST /register/provider-account` menerima mapping credential per env.
- `POST /register/host-policy` menyimpan policy default host.
- Data host/product/order tersimpan di sqlite (`HOST_RUTEBAYAR_DATABASE_DSN`) setelah migrasi.
- Dashboard `/ui` menampilkan daftar host, produk, dan order secara minimal.

# Fase 7 Acceptance Criteria

- Jika `HOST_RUTEBAYAR_UPSTREAM_BASE_URL` diisi, request ke `/host/{host_id}/payments` dan `/host/{host_id}/payments/{reference}` diproxy ke upstream `/api/v1/...`.
- Request ke upstream menyertakan header `X-Host-ID`.
- Path `/host/{host_id}/payments` mengikuti spec contract di `api/openapi.yaml`.

# Fase 8 Acceptance Criteria

- Semua endpoint dokumentasi contract diperbarui untuk memetakan fitur yang ada.
- Milestone fase 6-8 tercatat dan bisa dipakai untuk review PR berikutnya.
- Self-hosted operator bisa menjalankan service dengan default env + file db.
