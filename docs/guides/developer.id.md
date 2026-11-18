# Panduan Developer

English: [developer.md](developer.md). File bahasa Inggris adalah sumber utama; perbarui keduanya bersamaan.

Panduan lain: [Operator](operator.id.md) · [Platform Admin](platform-admin.id.md) · [Admin Klien](client-admin.id.md) · [Integrasi API](api-integration.id.md) · [Developer](developer.id.md)

Panduan ini untuk **developer/maintainer** yang menerima kode ini: apa fungsi tiap folder dan file, bagaimana sebuah request mengalir sampai database, cara menambah modul baru dengan generator, dan cara menguji serta merilis perubahan. Repositori: `go-gin-tenant-template` (modular monolith: Gin, GORM/PostgreSQL dengan Row-Level Security, PASETO v4, Cobra CLI `tenantctl`, Redis, SMTP, gRPC). Tata letak mengikuti panduan resmi Go [Organizing a Go module](https://go.dev/doc/modules/layout).

## 1. Peta proyek

| Lokasi | Fungsi |
|---|---|
| `cmd/server/main.go` | Binary REST API. Tipis: baca config, rakit dependensi, jalankan. Tanpa logika bisnis |
| `cmd/cli/main.go` | Binary `tenantctl` (CLI Cobra) |
| `cmd/grpc/` | Binary gRPC AuthService untuk layanan lain (verifikasi token + RBAC) |
| `cmd/worker/` | Placeholder worker (README) |
| `internal/app/app.go` | **Perakitan modul**: membuat semua handler dan chain middleware. Di sinilah modul baru didaftarkan (di atas komentar `// tenantctl:modules`) |
| `internal/config/` | Membaca environment, validasi, aturan khusus production |
| `internal/core/` | Kontrak dasar: `Repository` generik (CRUD, paginasi, filter `tenant_id` otomatis), entitas, error domain (`ErrNotFound`, `ErrConflict`) |
| `internal/modules/<fitur>/` | Satu folder per fitur (lihat bagian 3): `user`, `group`, `session` (login, refresh, reset password), `tenant`, `rbac`, `endpoint`, `file`, `attachment`, `appversion` |
| `internal/platform/` | Pustaka teknis yang dipakai modul: `auth` (PASETO), `password` (argon2id), `database` (koneksi, transaksi tenant, router DB per tenant), `tenant` (identitas tenant di context), `migrate`, `ratelimit`, `cache`/`ttlcache`, `mailer` (SMTP), `storage` (penyimpanan file), `xlsx` (baca/tulis Excel), `observability`, `logger`, `validator`, `idgen` (UUID v7), `messaging` |
| `internal/transport/rest/` | Lapisan HTTP: `router.go`, `middleware/` (request id, access log, authenticate, tenant tx, authorize), `response/` (envelope + katalog kode error), `request/` (bind, parse list), `crud/` (handler CRUD generik), `openapi/` |
| `internal/transport/grpc/` | Server gRPC; kode hasil generate ada di `gen/` |
| `internal/generator/` | Mesin `make:module` dan `make:migration` |
| `internal/cli/` | Perintah `tenantctl` (provisioning, migrate, make) |
| `migrations/` | Migrasi SQL bernomor `NNNNNN_nama.up.sql` / `.down.sql` (000001 sampai 000013 saat ini) |
| `api/openapi`, `api/proto` | Spesifikasi API dan definisi protobuf |
| `deployments/` | `docker/` (compose dev, Dockerfile) dan `k8s/` (base + overlay dev/prod) |
| `test/integration/` | Tes ke PostgreSQL dan Redis sungguhan (isolasi tenant, RBAC, auth, file, import, dll.) |
| `docs/` | Dokumentasi per topik + `docs/adr/` (keputusan arsitektur) + `docs/postman/` |
| `.ai/`, `CLAUDE.md`, `AGENTS.md` | Aturan dan skill untuk asisten AI dan kontributor |
| `scripts/` | `init-env.sh` (buat `.env.<mode>` + `PASETO_KEY` baru) dan git hook yang memblokir file env/kunci |
| `Makefile` | Semua perintah harian (`make help`) |

Prinsip: modul dikelompokkan per **fitur**, bukan per jenis (tanpa `controllers/`, `models/`, `utils/`, `pkg/`). `internal/` tidak bisa diimpor proyek lain, jadi API internal bebas direfaktor. Paket `platform/*` tidak boleh mengimpor `modules/*`; antar modul lewat interface.

## 2. Perjalanan sebuah request

Contoh: `GET /api/v1/users` oleh admin tenant.

1. **Router** (`transport/rest/router.go`) menerima request. `RequestID` membuat atau memakai `X-Request-ID`; `AccessLog` mencatat satu baris JSON per request.
2. **`Authenticate`** memverifikasi token PASETO dan menaruh `tenant_id` serta level dari **token** ke context (header/subdomain tidak dipercaya lagi setelah login).
3. **`TenantTx`** membuka satu transaksi database per request dan mengisi variabel sesi `app.current_tenant` dan `app.current_level` (`set_config(..., true)`, setara `SET LOCAL`). Commit bila status < 400, rollback bila tidak. Tanpa ini RLS gagal tertutup dan query tidak melihat baris apa pun.
4. **`Authorize`** mencocokkan `METHOD + pola rute Gin` (mis. `GET /api/v1/users`, bukan URL mentah) dengan izin grup pengguna. Default-deny: tidak ada izin berarti 403. Alasan penolakan masuk log server.
5. **Handler** (`modules/user/handler.go`) hanya menerjemahkan HTTP: bind body/query, panggil service, petakan error ke envelope. Tanpa aturan bisnis.
6. **Service** (`service.go`) berisi aturan bisnis (mis. tidak boleh menonaktifkan diri sendiri) dan bergantung pada **interface** repository.
7. **Repository** (`repository.go`) mengakses database lewat `core.Repository`, yang selalu menambahkan `WHERE tenant_id = ?`. PostgreSQL menegakkan lagi lewat RLS (`app_tenant()`).
8. **Response** dibungkus envelope `{success, code, data}`; error lewat `response.Fail(c, err)` menjadi `{success:false, code, error:{code,message,details}}`.

Dua lapis isolasi tenant bekerja bersamaan (filter aplikasi dan RLS); `test/integration` membuktikan masing-masing bekerja sendiri. Aplikasi harus terhubung sebagai role database **non-superuser tanpa BYPASSRLS**.

## 3. Anatomi sebuah modul

Contoh `internal/modules/user/`:

| File | Fungsi |
|---|---|
| `model.go` | Struct GORM dan method yang dibutuhkan `crud` (mis. `SetTenantID`) |
| `repository.go` | Query ke database; tambahkan query khusus di sini |
| `service.go` | Aturan bisnis; handler memanggil service, service memanggil repository |
| `handler.go` | Rute HTTP, DTO input, whitelist filter/sort; `RegisterRoutes(rg)` |
| `*_test.go` | Tes unit (tanpa database) dengan tabel kasus |

Modul yang lebih kaya menambah file sendiri, mis. `user/importer.go` + `sheet.go` (impor Excel), `session/reset.go` (reset password).

Pola **crud.Handler** menyediakan 7 rute standar per resource: `GET /x`, `GET /x/:id`, `GET /x/show_by/:column/:value`, `GET /x/all_by/:column/:value`, `POST /x`, `PATCH /x/:id`, `DELETE /x/:id` (soft delete). Aturan keamanannya: body di-decode ke **tipe input** (bukan model, mencegah mass assignment), hanya kolom whitelist yang bisa difilter/diurutkan, `tenant_id` hanya dari token, dan `TenantTx` wajib di setiap rute data tenant.

## 4. Menambah modul baru dengan generator

Generator menulis 90% yang membosankan dan sudah benar sejak awal: isolasi tenant (RLS), paginasi, filter, sort, soft delete, dan RBAC. Anda menambah aturan bisnis.

```bash
# lihat dulu apa yang akan dibuat (tidak menulis apa pun)
go run ./cmd/cli make:module product --field 'name:string!' --field 'price:bigint!' --field active:bool --unique name --dry-run

# buat sungguhan (lewat make)
make new-module name=product fields="name:string! price:bigint! active:bool note:text" unique=name
```

**Tipe field**: `string` (text, maks 255), `text` (maks 5000, tanpa filter/sort), `int`, `bigint` (pakai untuk uang dalam satuan terkecil), `float`, `bool`, `time` (RFC 3339), `uuid`. Tanda `!` = wajib (`NOT NULL`; kosong = 422). `--unique name` = unik per tenant di antara data yang belum dihapus (duplikat = 409). `--table` mengganti nama tabel (default: bentuk jamak nama modul).

**Yang ditulis generator**: migrasi `migrations/NNNNNN_create_<tabel>.up/down.sql` (kolom `<nama>_id` uuid, `tenant_id`, timestamp, `deleted_at`, indeks, trigger `updated_at`, `ENABLE`+`FORCE ROW LEVEL SECURITY`, policy `tenant_isolation`), `model.go`, `repository.go`, `service.go`, `handler.go`, `handler_test.go`, `test/integration/<nama>_test.go`, dan mendaftarkan modul di `internal/app/app.go`. Generator **tidak pernah menimpa**; bila ada file yang sudah ada, tidak ada yang ditulis.

**Setelah generate**

1. Baca migrasi; ubah bila kolom butuh tipe lain.
2. Terapkan: `make migrate-dev`.
3. `make sync-endpoints` agar rute baru masuk whitelist (sebelum diberi izin, rute menjawab 403).
4. Beri izin: `tenantctl group grant --tenant <kode> --group <grup> --all` atau `--endpoint "GET /api/v1/<tabel>"`.
5. `make test lint`, lalu `make test-integration` dengan database berjalan.

Nama modul: tunggal, huruf kecil dan angka (`product`, `invoice`); nama yang bentrok dengan paket template (`user`, `group`, `session`, `tenant`, ...) ditolak. Migrasi manual: `make new-migration name=add_price_to_products`. Rinci: `docs/generator.md`. Bila `app.go` tidak bisa dipatch (komentar penanda hilang), daftarkan manual seperti di `.ai/skills/create-module.md`.

## 5. Tabel, migrasi, dan RLS

- Kolom standar: `{tabel}_id` (UUID v7), `{tabel}_unique` (identity), `tenant_id`, `created_at`, `updated_at`, `deleted_at`. Tabel global (`endpoints`, `app_versions`) tanpa `tenant_id`.
- Unique constraint = *partial index* `WHERE deleted_at IS NULL` yang diawali `tenant_id`.
- Foreign key ke tabel tenant memakai **pasangan** `(tenant_id, kolom)` karena pemeriksaan FK PostgreSQL tidak tunduk pada RLS; tanpa itu tenant B bisa menunjuk data tenant A.
- Migrasi berjalan maju, tiap file dalam transaksinya sendiri: `make migrate-dev` (semua database), `make migrate-status`, atau `tenantctl migrate up\|status\|baseline`.
- Jangan menyusun SQL tenant dengan penggabungan string; gunakan parameter.

## 6. Perintah sehari-hari

| Perintah | Fungsi |
|---|---|
| `make env-init` | Buat `.env.development` dari contoh + `PASETO_KEY` baru (tidak menimpa) |
| `make docker-up` / `docker-down` | Jalankan/hentikan Postgres + Redis (`PROFILES="mail app"` untuk tambahan) |
| `make migrate-dev` | Terapkan migrasi ke semua database |
| `make sync-endpoints` | Daftarkan rute ke tabel `endpoints` |
| `make run` / `run-grpc` | Jalankan REST / gRPC |
| `make test`, `test-race`, `test-integration`, `cover` | Tes unit, dengan race detector, ke DB sungguhan, laporan cakupan |
| `make fmt`, `vet`, `lint`, `vuln` | Format, vet, golangci-lint v2, pemeriksaan kerentanan dependensi |
| `make new-module`, `new-migration` | Generator |
| `make keygen` | Nilai `PASETO_KEY` baru |
| `make build`, `docker-build` | Binary ke `./bin`, image aplikasi |
| `make proto` | Generate kode gRPC (butuh buf dan plugin) |

CLI `tenantctl` (`go run ./cmd/cli ...`): `tenant create`, `user create\|set-group`, `admin create`, `group create\|grant`, `endpoint sync [--prune]`, `migrate up\|status\|baseline`, `make:module`, `make:migration`, `keygen`.

## 7. Pengujian

- **Unit** (`make test`): tanpa database, tabel kasus; setiap perilaku baru wajib ada tesnya.
- **Integrasi** (`make test-integration`): PostgreSQL dan Redis sungguhan (`DB_*` dan `REDIS_PORT` dari `.env.<mode>`, atau `TEST_DATABASE_URL` / `TEST_REDIS_ADDR`). Mencakup isolasi tenant (tenant B tidak bisa melihat/mengubah data A), default-deny, auth, file, import, dan kecocokan REST dengan gRPC.
- **Collection Postman** (`docs/postman` dan berkas collection terbaru) dipakai untuk uji manual end-to-end.
- Sebelum PR: `make fmt vet lint test`; CI menjalankan lint, tes, govulncheck, dan gitleaks.

## 8. Konvensi wajib

Sumber: `CLAUDE.md` / `AGENTS.md`.

- Semua isi repositori (kode, komentar, dokumen, pesan error, commit, PR) dalam **bahasa Inggris**; pengecualian: `docs/getting-started.id.md`.
- Ikuti panduan resmi Go dan `gofmt`/`goimports`; `context.Context` argumen pertama; error dibungkus `%w`.
- Setiap query tabel tenant memakai `tenant_id` dari context (`tenant.IDFrom`), tidak pernah dari body/header.
- Primary key: `idgen.New()` (UUID v7); refresh token: `idgen.NewOpaqueToken()`.
- Log dengan `slog` (key-value); jangan pernah mencatat password, token, atau `PASETO_KEY`.
- Dependensi baru: periksa baris `go` di go.mod-nya (maks 1.26.0) dan catat di `docs/libraries.md`.
- Dilarang membaca/mengubah file `.env` asli, `git add -f`, melewati hook (`--no-verify`), `log.Fatal`/`panic` di luar `main`.
- Alur kerja: cabang fitur, `make test lint`, commit, push, buka PR, merge setelah CI hijau.

## 9. Memperluas sistem

- **Penyimpanan file ke S3/MinIO/GCS**: buat tipe baru yang memenuhi `storage.Store` (`Put`, `Open`, `Delete`) dan pilih lewat `STORAGE_DRIVER` di `cmd/server/main.go`. Kunci objek sudah aman.
- **Lampiran untuk modul lain**: pola `attachment` (`docs/attachments.md`): tabel `<induk>_documents` dengan FK komposit, lalu daftarkan `attachment.NewHandler(...)` di `app.go`.
- **Endpoint publik** (tanpa token): taruh di bawah `/api/v1/public/`; rute ini otomatis tidak masuk whitelist RBAC.
- **Rute khusus platform**: beri scope `platform` (lihat `docs/rbac.md` dan App Versions sebagai contoh).
- **Layanan lain** (template microservice berikutnya) memverifikasi token lewat gRPC AuthService, tanpa memegang `PASETO_KEY` (`docs/grpc.md`).
- Dokumentasi OpenAPI dibuat dari kode (`/openapi.json`, `/docs`); daftarkan operasi baru di handler dengan `openapi.Op`.

## 10. Dokumen rujukan

`docs/architecture.md`, `multi-tenancy.md`, `rbac.md`, `authentication.md`, `http-api.md`, `generator.md`, `files.md`, `attachments.md`, `app-versions.md`, `users-api.md`, `users-import.md`, `database-per-tenant.md`, `grpc.md`, `observability.md`, `openapi.md`, `environments.md`, `libraries.md`, serta `docs/adr/` untuk alasan di balik keputusan besar (ADR 0001 keputusan awal, ADR 0002 platform admin dan app version).
