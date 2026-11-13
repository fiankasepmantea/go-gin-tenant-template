# API user (`/api/v1/users`)

Administrator tenant mengelola user tenant-nya sendiri lewat API. Sebelumnya user hanya bisa dibuat lewat CLI
(`tenantctl user create`).

| Method | Path | Fungsi |
|---|---|---|
| GET | `/users` | daftar user (filter `username`, `email`, `name`, `is_active`, `group_id`; sort `username`, `email`, `name`, `created_at`; `page`, `per_page`, `from`, `to`) |
| GET | `/users/:id` | satu user |
| POST | `/users` | buat user: `username`, `email`, `name`, `password` (min 10 karakter), `group_id` (opsional), `is_active` (default true) |
| PATCH | `/users/:id` | ubah `email`, `name`, `group_id` (`""` = keluarkan dari group), `is_active` |
| DELETE | `/users/:id` | hapus (soft delete) |
| POST | `/users/:id/password` | admin mengatur password baru: `{"password": "..."}` |

## Aturan

- **Isolasi tenant**: tenant diambil dari token, bukan dari request. Repository menerapkan filter tenant dan
  PostgreSQL RLS menjaganya sekali lagi. User tenant lain tidak terlihat (404). Platform admin tidak muncul.
- **Password** tidak pernah dikembalikan. Password baru divalidasi kebijakan yang sama dengan ganti password
  (min 10, maks 1024 karakter) dan di-hash argon2id.
- Menonaktifkan (`is_active=false`), menghapus user, atau mengatur password baru **mengakhiri semua sesi** user
  itu (refresh token dicabut).
- Admin **tidak bisa menonaktifkan atau menghapus akun sendiri** (403) supaya tenant tidak kehilangan
  admin terakhir secara tidak sengaja.
- `username`/`email` unik per tenant (tidak peka huruf besar/kecil) -> 409 bila bentrok.
  `group_id` harus group milik tenant yang sama -> 422 bila tidak.
- Akses diatur RBAC seperti endpoint lain (default deny). Setelah deploy:

```bash
go run ./cmd/cli endpoint sync
go run ./cmd/cli group grant --tenant <kode> --group <nama> --all     # atau endpoint tertentu, mis. "GET /api/v1/users"
```

Group (`/api/v1/groups`) sudah punya CRUD penuh; hak akses group diatur lewat `/api/v1/groups/:id/endpoints`
(lihat `docs/rbac.md`).
