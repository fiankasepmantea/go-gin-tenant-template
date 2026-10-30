# ADR 0002: Admin platform dan app_version

Status: diterima

## Keputusan
1. Admin platform disimpan di `users` dengan `level='platform'` dan `tenant_id IS NULL`.
2. `app_versions` global, tanpa `tenant_id`.

## Alasan
1. Satu jalur login dan token untuk semua pengguna. Risiko baris ber-`NULL` ditutup di database: CHECK level/tenant,
   dua set unique parsial, policy RLS yang hanya membuka baris platform untuk sesi platform.
2. Satu aplikasi klien melayani semua tenant dan API-nya sama, jadi versi minimum yang didukung juga sama.

## Konsekuensi
- Pemisahan ke tabel sendiri dipertimbangkan lagi bila admin platform butuh MFA wajib atau IP allowlist.
- Aplikasi white-label: tambah `app_versions.tenant_id` nullable (NULL = default global).
- Aplikasi wajib memakai role database tanpa BYPASSRLS; pemilik tabel dan superuser melewati RLS.
