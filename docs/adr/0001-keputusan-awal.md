# 0001 — Keputusan awal

| Topik | Keputusan | Alasan |
|---|---|---|
| Isolasi tenant | Satu schema + `tenant_id`, resolver dapat ditukar | Paling ringan; jalur naik ke schema/DB per tenant tetap terbuka |
| Nama kolom | `tenant_id` | Lebih jelas daripada `domain_id` |
| Primary key | UUID v7 | Berurutan (ramah index), tipe `uuid` native; urutan mutlak oleh `{tabel}_unique` |
| Kolom standar | `{t}_id`, `{t}_unique`, `tenant_id`, `created_at`, `updated_at`, `deleted_at` | Kelanjutan konvensi CI4 + soft delete |
| Token | PASETO v4.local | Pengganti JWT tanpa pilihan algoritma yang bisa disalahgunakan |
| Admin | Dua level: platform dan tenant | Pemilik gedung vs penyewa |
| Go | `go 1.25.0` minimum | Lihat docs/libraries.md |
| Logging | `log/slog` | Stdlib, didukung OpenTelemetry |
| Migration | golang-migrate | Sudah dipakai di `simple-bank` |
