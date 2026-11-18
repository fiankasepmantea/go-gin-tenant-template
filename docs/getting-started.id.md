# Panduan memulai: cara memakai template ini

> English: [getting-started.md](getting-started.md)
>
> Dokumen ini adalah terjemahan Indonesia dari `getting-started.md`. Jika keduanya berbeda, versi Inggris
> yang berlaku. Seluruh dokumen lain di `docs/` tetap berbahasa Inggris, kecuali terjemahan `.id.md` di `docs/guides/`.

Panduan ini membawa Anda dari mesin kosong sampai API berjalan, menjelaskan dua jenis akun, dan menunjukkan alur kerja
sehari-hari (menambah modul, mengunggah file, melampirkannya ke data, menerbitkan versi aplikasi, mengimpor user).
Setiap perintah sudah dijalankan terhadap kode ini. Penjelasan yang lebih dalam ada di dokumen lain di `docs/`, tertaut di bagian akhir.

## 1. Apa yang Anda dapat

Backend **multi-tenant**: satu aplikasi melayani banyak pelanggan (tenant). Analogi: aplikasi adalah gedung apartemen
dan setiap tenant adalah satu penghuni.

| Lapisan | Fungsinya |
| --- | --- |
| Handler (`handler.go`) | Hanya HTTP: membaca request dan menulis response. Tanpa aturan bisnis. |
| Service (`service.go`) | Aturan bisnis. |
| Repository (`repository.go`) | Query database, selalu dibatasi ke tenant dari request. |
| PostgreSQL RLS | Tembok kedua: kesalahan di kode pun tidak bisa membaca baris milik tenant lain. |
| RBAC | Default-deny: user hanya bisa memanggil endpoint yang sudah diberikan ke grupnya. |

Teknologi: Go, Gin, GORM, PostgreSQL (row level security), token PASETO v4, Redis, SMTP, gRPC, CLI Cobra (`tenantctl`).

## 2. Dua jenis akun (baca ini dulu)

| | Platform admin | User tenant |
| --- | --- | --- |
| Milik | seluruh produk (tanpa tenant) | satu tenant |
| Dibuat dengan | `tenantctl admin create` | `tenantctl user create --tenant <kode>` (atau `POST /users` oleh admin tenant) |
| Login di | `POST /api/v1/auth/platform/login` (tanpa kode tenant) | `POST /api/v1/auth/login` dengan kode tenant |
| Boleh memanggil | hanya endpoint ber-scope `platform` (misalnya rute admin **App Versions**). Tidak perlu grant. | hanya endpoint ber-scope `tenant`, dan hanya yang sudah di-grant ke grup user |
| Contoh pemakaian | pemilik produk | staf milik pelanggan |

Token satu jenis tidak bisa dipakai untuk jenis lainnya: token tenant di rute platform dijawab `403`, dan sebaliknya.

Di Postman keduanya adalah dua set variabel yang terpisah: `username` / `password` (user tenant, misalnya `admin2`
di tenant `acme`) dan `platform_username` / `platform_password` (platform admin, misalnya `root`). Lihat bagian 11.

## 3. Prasyarat

Go (versi di `go.mod`), Docker, `make`. Dikembangkan di Linux dan WSL.

## 4. Menjalankan pertama kali, dari nol

```bash
git clone https://github.com/fiankasepmantea/go-gin-tenant-template.git
cd go-gin-tenant-template

make hooks          # sekali per clone: menolak commit file env dan kunci
make env-init       # membuat .env.development dengan PASETO_KEY baru (tidak pernah menimpa)
make docker-up      # PostgreSQL + Redis
go mod tidy         # hanya pertama kali
make migrate-dev    # membuat tabel-tabel
make sync-endpoints # menulis rute API ke tabel endpoints (daftar putih RBAC)
make run            # http://localhost:8080
```

Cek: `curl localhost:8080/healthz` menjawab `{"status":"ok"}`. API menjelaskan dirinya sendiri di
`http://localhost:8080/docs` (dokumen OpenAPI: `/openapi.json`).

File `.env.*` tidak pernah di-commit dan tidak pernah dibaca asisten AI. Ubah `.env.development` sendiri untuk mengganti
port atau password. Variabelnya dijelaskan di [environments.md](environments.md); file contohnya `.env.development.example`.

## 5. Membuat akun pertama

Tidak ada pendaftaran publik. Pakai CLI (`go run ./cmd/cli ...` atau `bin/tenantctl` setelah `make build`):

```bash
# sebuah tenant (pelanggan)
go run ./cmd/cli tenant create --code acme --name "Acme Inc"

# sebuah grup (peran) dan izinnya. Grup baru dimulai TANPA izin
go run ./cmd/cli group create --tenant acme --name admin --description "Full access"
go run ./cmd/cli group grant  --tenant acme --group admin --all

# sebuah user tenant di grup itu. Password dibaca dari stdin (bukan dari flag, supaya tidak tersimpan di riwayat shell)
read -rsp "Password: " PW; echo
printf '%s\n' "$PW" | go run ./cmd/cli user create --tenant acme --username admin --email admin@acme.test \
    --name "Acme Admin" --group admin --password-stdin

# sebuah platform admin (hanya perlu untuk rute platform seperti App Versions)
printf '%s\n' "$PW" | go run ./cmd/cli admin create --username root --email root@example.test \
    --name "Platform Admin" --password-stdin
unset PW
```

Password minimal 10 karakter. Setelah menambah rute (modul baru, atau versi template baru) jalankan
`make sync-endpoints` lagi dan berikan izin untuk rute barunya (lihat bagian 7).

> **Apa itu `platform_username` / `platform_password`?** Persis `--username` dan password yang Anda ketik saat
> `tenantctl admin create` (pada contoh: `root` dan password Anda). Jika Anda belum pernah membuat platform admin,
> jalankan perintah terakhir itu sekali.

## 6. Login dan memanggil API

```bash
# user tenant
curl -s localhost:8080/api/v1/auth/login -H 'Content-Type: application/json' -H 'X-Tenant-Code: acme' \
     -d '{"username":"admin","password":"<password>"}'

# jawabannya: {"access_token":"v4.local....","token_type":"Bearer","expires_in":3600,"refresh_token":"..."}
curl -s localhost:8080/api/v1/groups -H "Authorization: Bearer <access_token>"
```

- Tenant ditemukan dari subdomain, lalu header `X-Tenant-Code`, lalu `tenant_code` di body. Setelah login, tenant
  hanya berasal dari token.
- Access token berlaku selama `ACCESS_TOKEN_TTL` (1 jam di development, 15 menit di production). Pakai
  `POST /auth/refresh` dengan refresh token untuk mendapat token baru; refresh token hanya bisa dipakai sekali.
- Jawaban sukses berbentuk `{"success":true,"code":200,"data":...}`. Daftar dipaginasi:
  `?page=1&per_page=20&sort=-created_at&name=...`. Error berbentuk
  `{"success":false,"code":422,"error":{"code":"validation_failed","message":"...","details":[{"field":"...","rule":"..."}]}}`.

## 7. Alur kerja sehari-hari: menambah modul

```bash
go run ./cmd/cli make:module product --field 'name:string!' --field 'price:bigint!' --unique name
# atau: make new-module name=product fields="name:string! price:bigint!" unique=name
make migrate-dev        # terapkan migrasi baru
make sync-endpoints     # daftarkan rute baru
go run ./cmd/cli group grant --tenant acme --group admin --all
make test lint
```

Perintah ini menulis migrasi (tabel dengan RLS), model, repository, service, handler, dan tes, lalu mendaftarkan modulnya.
Setelah itu Anda menambah aturan bisnis di `service.go`. Sebelum grup diberi izin atas rutenya, rute menjawab `403`
(default-deny). Detail: [generator.md](generator.md), [rbac.md](rbac.md).

Endpoint yang dihasilkan untuk modul `product`: `GET/POST /api/v1/products`, `GET/PATCH/DELETE /api/v1/products/:id`,
`GET /products/show_by/:column/:value`, `GET /products/all_by/:column/:value`.

## 8. File, dan lampiran untuk sebuah data

**File** diunggah sekali dan dirujuk lewat id (`file_id`):

1. `POST /api/v1/files`: form multipart, field `file` (PNG, JPEG, GIF, WebP atau PDF, diperiksa dari isinya), field teks opsional
   `purpose` (satu kata huruf kecil pendek). Jawabannya berisi `file_id`.
2. `GET /api/v1/files/:id` (metadata), `GET /api/v1/files/:id/content` (unduh), `GET /api/v1/files` (daftar), `DELETE /api/v1/files/:id`.

Batas bawaan 10 MiB (`UPLOAD_MAX_MB`). File disimpan di `STORAGE_DIR` (`STORAGE_DRIVER=local`); S3 bisa menggantikannya
dengan mengimplementasikan satu interface kecil. Detail: [files.md](files.md).

**Lampiran (attachment)** menghubungkan file ke data milik modul Anda sendiri, misalnya foto sebuah kegiatan. URL-nya

```
/api/v1/<parent>/<parent_id>/<name>        contoh   /api/v1/activities/{{parent_id}}/documents
```

### Apa itu `parent_id`?

`parent_id` adalah **id dari data yang memiliki lampiran itu**, diambil dari modul yang ada di URL.

- Pada `/api/v1/activities/{{parent_id}}/documents`, `activities` adalah modul *buatan Anda* (template tidak membawanya;
  buat dengan `make:module activity`). `parent_id` adalah `activity_id` yang Anda dapat saat membuat sebuah kegiatan lewat
  `POST /api/v1/activities`.
- Pada tabel lama Anda `dokumentasi_kegiatan(kegiatan_id, ...)`: induknya adalah `kegiatan`, jadi `parent_id` adalah sebuah `kegiatan_id`.
- `documents` (`<name>`) adalah nama yang Anda pilih di `Config` saat mendaftarkan handler lampiran.
- Ia tidak dibuat oleh template dan tidak terisi otomatis di Postman: buat induknya dulu, lalu salin id-nya ke variabel
  `parent_id`.

Alurnya:

```text
1. POST   /api/v1/activities                                  -> {"activity_id": "A1"}      (induk)
2. POST   /api/v1/files            (multipart, file)           -> {"file_id": "F1"}
3. POST   /api/v1/activities/A1/documents   {"file_id":"F1"}   -> lampiran (attachment_id "D1")
4. GET    /api/v1/activities/A1/documents                      -> daftar, tiap item berisi name, content_type, size, url
5. DELETE /api/v1/activities/A1/documents/D1                   -> menghapus lampiran, file, dan isinya
```

Cara menghubungkannya ke tabel Anda (migrasi dengan foreign key gabungan, dan satu pendaftaran di `internal/app/app.go`)
ada langkah demi langkah di [attachments.md](attachments.md).

## 9. Versi aplikasi (update aplikasi mobile dan unduh APK)

Untuk aplikasi yang dibagikan di luar toko aplikasi. Bersifat global (bukan per tenant).

- **Admin** (hanya token platform admin): `/api/v1/app-versions` (buat, daftar, ambil, ubah, hapus) dan
  `PUT /api/v1/app-versions/:id/package` (multipart `file`: sebuah APK, untuk versi Android).
- **Publik** (tanpa token): `GET /api/v1/public/app-versions/latest?platform=android`,
  `GET .../check?platform=android&version=1.4.0` (menjawab `update_available` dan `force_update`),
  `GET .../latest/download?platform=android`, `GET .../:id/download` (mendukung lanjut unduh dengan `Range`).

Versi dibandingkan secara angka (`1.10.0` lebih baru dari `1.9.0`). Detail: [app-versions.md](app-versions.md).

## 10. Impor user dari Excel

`GET /api/v1/users/import/template` mengunduh template `.xlsx`. Isi (kolom `username`, `email`, `name`,
`password`; opsional `group` (nama grup yang sudah ada) dan `is_active`), lalu `POST /api/v1/users/import` dengan file di
field form `file`. Tambahkan `?dry_run=true` untuk hanya memeriksa. Sifatnya semua atau tidak sama sekali: jika satu baris salah,
tidak ada user yang dibuat dan jawabannya menyebut baris yang salah (`row 3 password: min`). Batas: 5 MiB, 500 user per file.
Detail: [users-import.md](users-import.md).

## 11. Postman

Impor `GO-GIN-TENANT-TEMPLATE.postman_collection.json` dan file environment-nya, pilih environment itu, lalu jalankan
**AUTH → Login** lebih dulu (ia menyimpan token).

| Variabel | Artinya |
| --- | --- |
| `base_url` | `http://localhost:8080/api/v1` **tanpa garis miring di akhir** (garis miring di akhir menghasilkan `//` dan 404) |
| `root_url` | `http://localhost:8080` (health, metrics, openapi) |
| `tenant_code` | tenant tujuan login, misalnya `acme` |
| `username`, `password` | **user tenant** dari tenant itu (bagian 5: `admin`; password yang Anda pilih) |
| `platform_username`, `platform_password` | **platform admin** (bagian 5: `root`; password yang Anda pilih). Hanya untuk request admin App Versions |
| `access_token`, `refresh_token` | disimpan oleh Login |
| `platform_token` | disimpan oleh Login (platform admin) |
| `group_id`, `user_id`, `file_id`, `version_id` | disimpan oleh request Create |
| `parent_id`, `attachment_id` | untuk contoh lampiran: diisi manual (lihat bagian 8) |
| `metrics_token` | hanya jika server memakai `METRICS_TOKEN` |

Variabel rahasia (`password`, token) harus diisi di environment (ikon gembok). Untuk request file, di tab Body pilih form-data
dan beri key `file` tipe **File**.

## 12. Development dan production

`APP_ENV=development` (bawaan) atau `production`. Production mewajibkan `DB_PASSWORD`, menolak `DB_SSLMODE=disable`, mencatat log JSON, dan
memakai access token 15 menit. Di production pakai environment proses (secret manager), bukan file env.
Build dan jalankan: `make build`, `make docker-build`, manifest di `deployments/`. Detail: [environments.md](environments.md).

Tenant besar atau teregulasi bisa mendapat database sendiri (hybrid): [database-per-tenant.md](database-per-tenant.md).
Saat pelanggan berhenti, nonaktifkan, ekspor, dan hapus datanya dengan `tenantctl tenant suspend|export|delete`: [offboarding.md](offboarding.md).

## 13. Pemeriksaan mutu

```bash
make test               # tes unit
make test-race          # dengan race detector
make test-integration   # terhadap PostgreSQL dan Redis sungguhan
make lint               # golangci-lint
make vuln               # govulncheck
```

CI (GitHub Actions) menjalankan lint, test, integration, pemindaian kerentanan, pemindaian secret, penjaga file env, dan build Docker pada setiap
pull request. Merge hanya jika semuanya hijau.

## 14. Pemecahan masalah

| Gejala | Penyebab dan perbaikan |
| --- | --- |
| `connect ECONNREFUSED 127.0.0.1:8080` | Server belum berjalan: `make run`. Cek `curl localhost:8080/healthz`. |
| `404` di setiap request Postman | `base_url` berakhiran `/`. Hapus garis miring di akhir. |
| `401 invalid_credentials` saat login | Kode tenant, user, atau password salah (semuanya sengaja terlihat sama), user nonaktif, atau tenant sedang disuspend. |
| `401` di request lain | Access token tidak ada atau kedaluwarsa: jalankan Login (atau Refresh) lagi, dan set Bearer token. |
| `403 forbidden` di rute biasa | Grup user belum diberi izin atas endpoint itu. `make sync-endpoints`, lalu `group grant` (atau API `/groups/:id/permissions`). Alasannya ada di log server (`access_denied`: `not_granted`, `no_group`, `wrong_level`). |
| `403` di rute admin App Versions | Anda memakai token tenant. Login dengan **Login (platform admin)**. |
| `422 validation_failed` | Baca `error.details`: field dan aturannya. Password minimal 10 karakter. |
| `409 conflict` | Data sudah ada (misalnya username atau versi duplikat). |
| `413` | File lebih besar dari `UPLOAD_MAX_MB` (APK: `UPLOAD_APK_MAX_MB`). |
| `429 rate_limited` | Terlalu banyak login gagal; tunggu sesuai `Retry-After`. |
| `tenant does not exist` di CLI | CLI memakai database dari `.env.<mode>` yang aktif; buat tenant di sana dengan `tenant create`. |

## 15. Bacaan lanjutan

Dokumen berikut berbahasa Inggris, kecuali panduan di `guides/` yang punya terjemahan `.id.md`.

| Topik | Dokumen |
| --- | --- |
| Panduan per peran (operator, platform admin, admin klien, integrasi API, developer) | [guides/](guides/developer.id.md) |
| Arsitektur dan tata letak | [architecture.md](architecture.md) |
| Autentikasi, token, rate limiting | [authentication.md](authentication.md) |
| RBAC | [rbac.md](rbac.md) |
| Konvensi HTTP (envelope, filter, error) | [http-api.md](http-api.md) |
| Multi-tenancy dan RLS | [multi-tenancy.md](multi-tenancy.md) |
| Generator | [generator.md](generator.md) |
| API user dan impor | [users-api.md](users-api.md), [users-import.md](users-import.md) |
| File dan lampiran | [files.md](files.md), [attachments.md](attachments.md) |
| Versi aplikasi | [app-versions.md](app-versions.md) |
| gRPC, observability, OpenAPI | [grpc.md](grpc.md), [observability.md](observability.md), [openapi.md](openapi.md) |
| Database per tenant | [database-per-tenant.md](database-per-tenant.md) |
| Offboarding klien | [offboarding.md](offboarding.md) |
