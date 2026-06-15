# Callback & Observability Playbook

## Contoh envelope callback ke host

```json
{
  "reference": "pay_123456",
  "status": "success",
  "host_id": "host-demo",
  "product_id": "prod-001",
  "buyer_ref": "order-001",
  "gross_amount": 120000,
  "host_fee_amount": 2400,
  "provider_fee_amount": 3000,
  "net_amount": 114600,
  "policy": {
    "policy_version": "v1",
    "policy_payload_hash": "sha256:..."
  }
}
```

## Verifikasi callback per host

- `POST` ke host callback URL dari `callback_urls`.
- Kirim header `X-Host-ID` dan signature timestamp bila host memakai middleware verifikasi.
- Terapkan idempotency di endpoint host (simpan `reference` + provider status).

## Checklist observability yang disiapkan

- `payments.create` counter dan error labels.
- `payments.webhook` success/error + invalid signature ratio.
- DLQ drain log saat retry/reconciliate gagal.
- Monitoring puncak latency: waktu dari `create` sampai webhook success.
- Audit log: create-payment, webhook success, reconcile status, dan callback failure.

## Playbook alert

- Jika webhook invalid signature naik >5% selama 5 menit, matikan forward callback sementara dan minta rotasi secret.
- Jika DLQ tumbuh terus >100 item, cek kejanggalan endpoint callback host.
- Jika jumlah `payments.create.error.product_inactive` tinggi, cek onboarding produk yang belum aktif.
