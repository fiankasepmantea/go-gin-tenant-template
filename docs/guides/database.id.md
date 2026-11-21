# Panduan database dan CLI

> English: [database.md](database.md). Dokumen ini terjemahan Indonesia. Jika keduanya berbeda, versi Inggris yang berlaku.

Untuk maintainer dan operator. Isinya: fungsi tiap tabel, arti kolomnya, cara CLI `tenantctl` dan server bekerja
bersama, rute mana yang butuh login, dan bagaimana beberapa tenant berbagi (atau tidak berbagi) database.
Sumber: `migrations/*.up.sql`, `internal/cli`, `internal/modules/endpoint`, `docs/multi-tenancy.md`, `docs/rbac.md`,
`docs/database-per-tenant.md`.

## 1. Kolom yang ada di setiap tabel

| Kolom | Arti |
|---|---|
| `<tabel>_id` | Kunci utama, UUID v7 (`idgen.New()`). Ini id yang tampil di API. |
| `<tabel>_unique` | Nomor identity. Memberi urutan mutlak yang stabil. Tidak pernah dipakai sebagai id. |
| `tenant_id` | Pemilik baris. Ada di setiap tabel tenant, selalu kolom pertama indeks. |
| `created_at`, `updated_at` | Waktu. `updated_at` diisi trigger `set_updated_at()`. |
| `deleted_at` | Soft delete. Baris yang terisi dianggap terhapus. Indeks unik bersifat parsial: `WHERE deleted_at IS NULL`. |

## 2. Tabel

| Tabel | Fungsi | Terpisah per tenant (RLS) |
|---|---|---|
| `tenants` | Organisasi pelanggan | Tidak (milik platform) |
| `groups` | Peran di dalam satu tenant | Ya |
| `users` | Semua akun: user tenant dan admin platform | Ya (baris platform hanya terlihat oleh sesi platform) |
| `endpoints` | Daftar putih rute API | Tidak (global) |
| `group_endpoints` | Grup mana boleh memanggil endpoint mana | Ya |
| `user_tokens` | Refresh token | Ya |
| `password_resets` | Permintaan reset kata sandi | Ya |
| `files` | Metadata file unggahan | Ya |
| `app_versions` | Versi aplikasi klien | Tidak (global) |

### `tenants`
| Kolom | Arti |
|---|---|
| `code` | Nama pendek untuk login (`acme`). Huruf kecil, angka, dan `-`; unik di antara tenant aktif. |
| `name` | Nama tampilan. |
| `status` | `active` atau `suspended`. Tenant yang ditangguhkan tidak bisa login. |
| `db_ref` | `NULL`: tenant ada di database bersama. Berisi nilai (`acme`): database khusus, URL-nya ada di variabel lingkungan `TENANT_DB_URL_ACME` (tidak pernah disimpan di tabel). |

### `groups`
`name` (unik per tenant, tidak peduli huruf besar-kecil) dan `description`. Grup tidak punya izin sampai Anda memberinya.

### `users`
| Kolom | Arti |
|---|---|
| `level` | `tenant` atau `platform`. Sebuah check menjaga `level` dan `tenant_id` tetap konsisten: admin platform tidak punya tenant dan grup. |
| `group_id` | Peran user. Foreign key gabungan `(tenant_id, group_id)` membuat user mustahil menunjuk grup milik tenant lain. |
| `username`, `email` | Unik per tenant, tidak peduli huruf besar-kecil (dua set indeks untuk user tenant dan admin platform, karena `NULL` dianggap berbeda di indeks unik biasa). |
| `name` | Nama tampilan. |
| `password_hash` | Hash argon2id format PHC. Tidak pernah dikirim API. |
| `is_active` | `false` memblokir login dan mengakhiri semua sesi. |

### `endpoints` dan `group_endpoints`
`endpoints` berisi satu baris per rute API: `method` + `path` (pola Gin, misalnya `GET /api/v1/users/:id`), `name`,
`scope` (`tenant` atau `platform`), dan `is_public` (lihat bagian 5). Tabel ini global dan diisi oleh
`tenantctl endpoint sync` dari rute di kode. `group_endpoints` menghubungkan `(tenant_id, group_id)` ke
`endpoint_id`: inilah daftar izin sebuah grup. Detail: [../rbac.md](../rbac.md).

### `user_tokens`
Satu baris per refresh token: `user_id`, `token_hash` (SHA-256, token aslinya tidak pernah disimpan), `user_agent`,
`ip_address`, `expires_at`, `revoked_at`. Token hanya berlaku sekali; memakai token yang sudah dipakai mencabut semua sesi user.

### `password_resets`
`user_id`, `token_hash` (SHA-256), `ip_address`, `expires_at`, `used_at`. Sekali pakai dan berumur pendek.

### `files`
Hanya metadata: `name`, `content_type`, `size`, `purpose` (label pendek seperti `avatar`), `uploaded_by`, dan
`storage_key`, yaitu kunci isi file di penyimpanan (folder lokal secara bawaan). `(tenant_id, file_id)` unik, sehingga
tabel lain bisa merujuk file dengan foreign key gabungan dan tidak pernah menunjuk file tenant lain.

### `app_versions`
Daftar global versi aplikasi klien: `platform` (`android`, `ios`, `web`), `version`, `min_supported_version`,
`is_force_update`, `release_notes`, `store_url`, dan APK opsional (`package_key`, `package_size`, `package_sha256`,
terisi bersamaan atau tidak sama sekali). Endpoint cek versi bersifat publik.

## 3. Cara tenant dipisahkan

Dua lapis, sehingga bug di satu lapis tertangkap lapis lain:

1. Repository menambahkan `WHERE tenant_id = ?` dari context. Tenant selalu berasal dari token yang sudah diverifikasi,
   tidak pernah dari body atau header (header hanya dipakai saat login).
2. PostgreSQL row level security: tiap tabel tenant punya policy `tenant_isolation` yang membandingkan `tenant_id`
   dengan `app_tenant()`. Tanpa tenant di sesi, hasilnya nol baris (fail closed). Aplikasi terhubung sebagai role tanpa `BYPASSRLS`.

## 4. Satu skema, atau skema (database) per tenant?

| Model | Artinya | Status |
|---|---|---|
| **Skema bersama** (bawaan) | Satu database, satu set tabel. Baris semua tenant bercampur dan dipisah oleh `tenant_id` dan RLS. | Sudah ada, bawaan |
| **Database per tenant** | Tenant punya database sendiri. Kode dan migrasi sama. Buat tenant dengan `--db-ref`. | Sudah ada (pilot), lihat [../database-per-tenant.md](../database-per-tenant.md) |
| **Skema per tenant** (schema Postgres dalam satu database) | Tiap tenant punya schema sendiri. | **Belum dibuat.** Tenant resolver bisa menampungnya, tetapi belum ada kodenya. |

Dengan beberapa tenant di database bersama tidak ada yang digandakan: tabel atau kolom baru dibuat sekali dan langsung
tersedia untuk semua tenant. Yang terpisah hanya datanya. Database khusus untuk tenant besar atau teregulasi yang harus
terpisah secara fisik; biaya operasinya lebih tinggi (pool, backup, migrasi per database).

## 5. Rute mana yang butuh login

| Jenis | Butuh token | Butuh izin grup | Cara membuatnya |
|---|---|---|---|
| **Terbuka** | Tidak | Tidak | Pasang di bawah `/api/v1/public/...` (atau `/auth/...`), di luar rantai middleware. Lihat `appversion/handler.go`, `RegisterRoutes`. Tidak ada yang perlu disinkronkan atau di-grant. |
| **Cukup login** (`is_public`) | Ya | Tidak | Tambahkan `"GET /api/v1/x": {IsPublic: true}` ke `overrides` di `internal/modules/endpoint/endpoint.go`, lalu `endpoint sync`. |
| **Perlu izin** (bawaan) | Ya | Ya | Semua rute buatan generator atau `crud.Handler`. Ditolak 403 sampai grupnya diberi izin. |
| **Khusus platform** | Ya (admin platform) | Tidak | `{Scope: ScopePlatform}` di `overrides`. |

Aturan praktis: rute terlindungi kecuali sengaja Anda taruh di bawah `/public/` atau `/auth/`. Generator tidak pernah
membuat rute terbuka.

## 6. Bekerja dengan CLI

Dua terminal, satu database (yang tertulis di `.env.<mode>`):

| Terminal | Menjalankan | Umur |
|---|---|---|
| 1 | `make run`: server REST | terus berjalan |
| 2 | `tenantctl ...` (`go run ./cmd/cli ...` atau `bin/tenantctl`): provisioning, migrasi, generator | satu perintah, lalu selesai |

CLI langsung berbicara ke database, jadi bisa dipakai saat server mati. Server tidak tahu ada kode atau rute baru
sampai Anda menjalankannya ulang, karena `go run` sudah mengompilasi kode lama.

### Membuat tabel dan endpoint-nya (modul baru)

```bash
# terminal 2
make new-module name=product fields="name:string! price:bigint!" unique=name   # kode, migrasi, tes
make migrate-dev                                                               # membuat tabel
make sync-endpoints                                                            # mendaftarkan 7 rute
go run ./cmd/cli group grant --tenant acme --group admin --all                 # memberi izin ke grup
# terminal 1: hentikan server (Ctrl+C) lalu jalankan `make run` lagi
```

Generator hanya menulis rute CRUD standar. Rute kustom ditulis manual di `handler.go` modul (`RegisterRoutes`); lalu
jalankan `make sync-endpoints` dan beri izin. Tidak ada perintah CLI yang membuat rute kustom.
`make sync-endpoints` membaca rute dari kode, jadi harus dijalankan setelah kodenya ada.

### Beberapa tenant

Alat dan terminalnya sama. Tenant dipilih dengan `--tenant` di setiap perintah:

```bash
tenantctl tenant create --code acme --name "Acme"
tenantctl tenant create --code budi --name "Budi"
tenantctl group create --tenant acme --name admin
tenantctl group grant  --tenant acme --group admin --all
tenantctl group create --tenant budi --name admin
tenantctl group grant  --tenant budi --group admin --all
printf '%s\n' "$PW" | tenantctl user create --tenant budi --username owner --email o@budi.test --name Owner --group admin --password-stdin
```

| Langkah | Per tenant atau global |
|---|---|
| `make migrate-dev` | Global: sekali untuk semua tenant di database bersama (dan semua database khusus) |
| `make sync-endpoints` | Global |
| `group create`, `group grant`, `user create`, `user set-group` | Per tenant (`--tenant`) |
| `admin create` | Level platform, tanpa `--tenant` |
| `tenant export`, `tenant delete` | Per tenant (`--code`) |

Grup `admin` di tenant `acme` dan grup `admin` di tenant `budi` adalah baris berbeda dengan izin berbeda.

### Ringkasan perintah

`tenant create|export|delete`, `group create|grant`, `user create|set-group`, `admin create`, `endpoint sync [--prune]`,
`migrate up|status|baseline [--ref R | --all]`, `make:module`, `make:migration`, `keygen`, `version`.
Kata sandi dibaca dari stdin (`--password-stdin`), tidak pernah dari flag.
