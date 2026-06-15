# Phase 0 Acceptance Criteria

- `Load()` membaca konfigurasi host, port, environment, timeout.
- Bila env tidak tersedia, default tetap bekerja (`development`, `127.0.0.1:8080`, 10s).
- Pengaturan yang invalid akan diabaikan dengan fallback default agar runtime tetap stabil.
- Struktur paket siap untuk implementasi domain dan orchestrator pada phase berikutnya.
