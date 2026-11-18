# Panduan Operator

English: [operator.md](operator.md). File bahasa Inggris adalah sumber utama; perbarui keduanya bersamaan.

Panduan lain: [Operator](operator.id.md) · [Platform Admin](platform-admin.id.md) · [Admin Klien](client-admin.id.md) · [Integrasi API](api-integration.id.md) · [Developer](developer.id.md)

Panduan ini untuk operator/tim IT yang menjalankan aplikasi di server (staging atau production). Perintah memakai Docker Compose sebagai contoh utama; manifest Kubernetes ada di `deployments/k8s` (`base`, `overlays/dev`, `overlays/prod`). Hosting belum ditentukan, jadi langkah dibuat netral.

## 1. Gambaran sistem

| Komponen | Fungsi | Wajib? |
|---|---|---|
| API server (`/server`, port 8080) | REST API `/api/v1`, `/healthz`, `/readyz`, `/metrics`, `/docs` | Ya |
| PostgreSQL 17 | Data semua tenant (satu schema `public`, kolom `tenant_id`, Row-Level Security) | Ya |
| Redis 8 | Rate limit login (wajib bila replika > 1) | Production: ya |
| SMTP | Email lupa/reset password | Bila fitur reset dipakai |
| Volume file (`STORAGE_DIR`) | File upload dan APK | Ya, harus persisten |
| gRPC AuthService (`/auth-grpc`, port 9090) | Verifikasi token untuk layanan lain | Opsional |
| Database khusus tenant (`TENANT_DB_URL_<REF>`) | Tenant yang butuh database terpisah | Opsional |

Image runtime berbasis distroless, berjalan sebagai non-root, dan hanya berisi `/server` dan `/auth-grpc`. CLI `tenantctl` **tidak** ada di image; jalankan dari mesin build/CI/bastion yang bisa menjangkau database (`go run ./cmd/cli ...` atau `go build -o tenantctl ./cmd/cli`).

## 2. Prasyarat

- Docker + Docker Compose (atau cluster Kubernetes), Go 1.25+ untuk menjalankan `tenantctl`.
- PostgreSQL dengan role aplikasi **bukan superuser dan tanpa BYPASSRLS** (superuser/pemilik tabel melewati RLS, isolasi tenant jadi tidak berlaku).
- Domain + sertifikat TLS di depan server (reverse proxy / load balancer). API sendiri melayani HTTP biasa.
- Server SMTP bila reset password dipakai.

## 3. Konfigurasi dan rahasia

Template: `.env.production.example` (tanpa nilai rahasia). Di server/Kubernetes isi lewat environment proses, ConfigMap + Secret, atau secret manager. **Jangan** commit file env asli.

| Variabel | Catatan production |
|---|---|
| `APP_ENV=production` | Mengaktifkan aturan ketat di bawah |
| `DB_HOST`, `DB_PORT`, `DB_USER`, `DB_NAME`, `DB_SCHEMA` | `DB_PASSWORD` **wajib**, aplikasi menolak start bila kosong |
| `DB_SSLMODE` | `require` atau `verify-full`; `disable` ditolak |
| `PASETO_KEY` | **Wajib**, beda dari development. Buat dengan `make keygen` (`tenantctl keygen`) |
| `ACCESS_TOKEN_TTL` / `REFRESH_TOKEN_TTL` | Default production `15m` / `720h` |
| `REDIS_ADDR`, `REDIS_PASSWORD` | Wajib bila replika lebih dari satu |
| `RATE_LIMIT_ATTEMPTS` / `RATE_LIMIT_WINDOW` | `5` percobaan per `15m` |
| `SMTP_HOST`, `SMTP_PORT`, `SMTP_USERNAME`, `SMTP_PASSWORD`, `SMTP_FROM`, `SMTP_SECURITY` | `starttls` atau `tls`; `none` ditolak |
| `PASSWORD_RESET_URL`, `PASSWORD_RESET_TTL` | URL harus `https`, placeholder `{token}` dan `{tenant}`; TTL default `30m` |
| `TENANT_HEADER`, `TENANT_BASE_DOMAIN` | Default `X-Tenant-Code`; mis. `app.example.com` agar `tokoa.app.example.com` dikenali sebagai tenant `tokoa` |
| `STORAGE_DRIVER=local`, `STORAGE_DIR`, `UPLOAD_MAX_MB`, `UPLOAD_APK_MAX_MB` | `STORAGE_DIR` di volume persisten; default 10 MB dan 200 MB |
| `DOCS_ENABLED` | Mati secara default; bila dinyalakan, lindungi rutenya di proxy |
| `METRICS_ENABLED`, `METRICS_TOKEN` | Token minimal 16 karakter di production |
| `GRPC_*` | TLS wajib; `GRPC_ALLOW_PLAINTEXT=true` hanya di belakang service mesh yang sudah mTLS |
| `LOG_LEVEL` | `info`; log berformat JSON di production |

Di Kubernetes, nilai non-rahasia ada di `configmap.yaml` (per environment lewat overlay), rahasia (`DB_USER`, `DB_PASSWORD`, `PASETO_KEY`, `REDIS_PASSWORD`, `SMTP_*`) ada di Secret; `secret.example.yaml` hanya contoh, pakai External Secrets/Sealed Secrets atau secret manager cloud.

**PASETO_KEY adalah rahasia terpenting.** Siapa pun yang memegangnya bisa membuat token valid. Bila bocor: buat kunci baru, deploy ulang (semua sesi login otomatis tidak berlaku, pengguna login ulang), dan cabut kunci lama dari semua tempat.

## 4. Pertama kali dijalankan

Urutan penting; ulangi tiap environment.

1. **Siapkan database dan role** aplikasi (tanpa BYPASSRLS).
2. **Migrasi**: `go run ./cmd/cli migrate up --all` (database bersama + semua database khusus tenant). Cek dengan `migrate status`. Tiap file migrasi berjalan di transaksi sendiri dengan lock, aman dijalankan ulang.
3. **Daftarkan endpoint ke RBAC**: `go run ./cmd/cli endpoint sync` (`make sync-endpoints`). Rute baru **ditolak** sampai diberi izin ke sebuah grup (default-deny).
4. **Buat platform admin pertama**: `tenantctl admin create ...` (lihat Panduan Platform Admin).
5. **Jalankan server**, lalu verifikasi bagian 6.
6. Onboarding tenant pertama mengikuti Panduan Platform Admin.

Contoh Docker Compose: `deployments/docker/docker-compose.yml` (postgres, redis; profile opsional `mail` (Mailpit), `app`, `grpc`, `kafka`, `rabbitmq`). File itu untuk **development**; untuk production gunakan database terkelola atau volume dan backup sendiri.

```bash
docker compose -f deployments/docker/docker-compose.yml --profile app up -d --build
```

## 5. Reverse proxy dan jaringan

- Terminasi TLS di proxy/load balancer; teruskan ke port 8080.
- Batasi akses `/metrics` dan `/docs` (hanya jaringan internal, plus `METRICS_TOKEN`).
- Rute `/api/v1/public/*` (cek versi aplikasi, unduh APK) memang publik: pasang rate limit/CDN di depannya, terutama untuk unduhan APK (mendukung Range dan ETag).
- Body request dibatasi 1 MiB (endpoint auth 16 KiB) kecuali endpoint upload; batas upload ada di `UPLOAD_MAX_MB` dan `UPLOAD_APK_MAX_MB`, samakan dengan batas body di proxy.
- Teruskan header `X-Request-ID` bila proxy membuatnya; bila tidak, server membuatnya sendiri.
- Port PostgreSQL dan Redis jangan dibuka ke internet.

## 6. Verifikasi setelah deploy

| Pemeriksaan | Harapan |
|---|---|
| `GET /healthz` | 200, proses hidup (liveness) |
| `GET /readyz` | 200 bila database (dan database khusus tenant, `tenant_databases`) terjangkau (readiness) |
| `POST /api/v1/auth/platform/login` | 200 dengan token |
| `GET /metrics` (dengan token) | Format Prometheus |
| Log | JSON, satu baris per request dengan `request_id` |

Kubernetes: liveness ke `/healthz`, readiness ke `/readyz` sudah ada di `deployment.yaml` (2 replika, HPA 2 sampai 10 pada CPU 70%, `readOnlyRootFilesystem`; karena itu `STORAGE_DIR` harus berupa volume yang di-mount).

## 7. Upgrade versi

1. Baca catatan rilis dan migrasi baru (`migrations/*.up.sql`).
2. **Backup dulu** (bagian 8).
3. Jalankan `migrate up --all` **sebelum** versi baru menerima trafik.
4. Jalankan `endpoint sync` sebelum versi baru menerima trafik. Rute baru belum punya izin: berikan ke grup yang tepat (`tenantctl group grant ...`, atau `--all` untuk grup tertentu) atau pengguna akan mendapat 403 pada fitur baru. Rute yang sudah dihapus dilaporkan; `--prune` menghapus barisnya.
5. Deploy image baru (rolling update). Pantau `/readyz` dan metrik error.
6. Rollback: kembalikan image lama. Migrasi hanya maju; rollback skema butuh restore backup, jadi rencanakan migrasi yang kompatibel mundur.

## 8. Backup dan pemulihan

Ada dua hal yang harus dibackup **bersamaan**: database dan folder file.

- **PostgreSQL**: `pg_dump -Fc` terjadwal (harian minimal), atau backup/PITR dari penyedia database terkelola. Jangan lupa semua database khusus tenant (`TENANT_DB_URL_*`).
- **File upload** (`STORAGE_DIR`): snapshot volume atau rsync terjadwal. Tabel menyimpan metadata, file fisiknya hanya ada di sini; database tanpa file (atau sebaliknya) menghasilkan lampiran rusak.
- **Rahasia** (`PASETO_KEY`, password DB, SMTP) disimpan di secret manager, bukan di backup database.
- **Uji restore** berkala ke environment terpisah: restore dump, `migrate status`, `GET /readyz`, login, unduh satu file.

## 9. Monitoring dan log

- **Log**: JSON, tiap request punya `request_id` (juga dikembalikan di header `X-Request-ID`). Saat pengguna melapor error, minta `X-Request-ID` dari respons dan cari di log.
- **Penolakan RBAC** tercatat di log server dengan alasan: `not_granted` (grup belum diberi izin), `no_group` (pengguna tanpa grup), `wrong_level` (tenant memanggil rute platform atau sebaliknya), `unregistered_endpoint` (lupa `endpoint sync`).
- **Metrik**: `/metrics` (Prometheus). Alarm yang disarankan: rasio 5xx, latensi p95, `/readyz` gagal, kegagalan SMTP, ruang disk `STORAGE_DIR`, koneksi database. Tracing belum ada.
- Lihat `docs/observability.md` untuk nama metrik.

## 10. Operasi tenant dengan database khusus

Tenant besar dapat memakai database sendiri: set `TENANT_DB_URL_<REF>` (sslmode `require`, `verify-ca` atau `verify-full`), buat tenant dengan `tenantctl tenant create --db-ref <REF>`, migrasi dengan `migrate up --ref <REF>` (atau `--all`). Database yang migrasinya pernah dijalankan manual dapat dicatat dengan `migrate baseline`. Detail: `docs/database-per-tenant.md`.

## 11. Checklist keamanan production

- `APP_ENV=production`, `DB_SSLMODE=require/verify-full`, `DB_PASSWORD` terisi
- `PASETO_KEY` unik per environment, tersimpan di secret manager
- Role database bukan superuser dan tanpa BYPASSRLS
- Redis aktif bila replika > 1
- TLS di proxy; `/metrics` dan `/docs` tidak terbuka ke publik
- `METRICS_TOKEN` >= 16 karakter
- SMTP memakai `starttls`/`tls`; `PASSWORD_RESET_URL` https
- gRPC memakai mTLS (atau service mesh)
- Backup database + file, dan restore sudah pernah diuji
- Tidak ada file `.env`/kunci di repository; kode diserahkan ke klien lewat repository milik klien (lihat `SECURITY.md`)
- Rahasia yang pernah bocor sudah dirotasi

## 12. Pemecahan masalah

| Gejala | Penyebab umum | Tindakan |
|---|---|---|
| Server tidak mau start di production | `DB_PASSWORD` kosong, `DB_SSLMODE=disable`, `SMTP_SECURITY=none`, `METRICS_TOKEN` pendek | Baca pesan error di log, perbaiki variabel |
| `/readyz` gagal | Database/ database tenant tidak terjangkau | Cek jaringan, kredensial, `TENANT_DB_URL_*` |
| 403 pada fitur baru setelah upgrade | Rute belum disinkronkan atau belum diberi izin | `endpoint sync`, lalu `group grant`; cek alasan di log |
| 429 `rate_limited` saat login | 5 percobaan gagal per 15 menit | Tunggu sesuai header `Retry-After`; tanpa Redis tiap replika menghitung sendiri |
| Email reset tidak sampai | `SMTP_HOST` kosong (endpoint tetap menjawab 202) | Isi SMTP; cek log |
| Upload ditolak | Melebihi `UPLOAD_MAX_MB`/`UPLOAD_APK_MAX_MB` atau batas proxy; tipe tidak diizinkan | Samakan batas; tipe yang diizinkan PNG, JPEG, GIF, WebP, PDF |
| File hilang setelah redeploy | `STORAGE_DIR` bukan volume persisten | Mount volume, restore dari backup |
| Semua pengguna logout mendadak | `PASETO_KEY` berubah | Sesuai desain; login ulang |
| Data tenant tidak terlihat | Token/tenant salah, atau role DB melewati RLS | Periksa tenant di login dan role database |

Setiap insiden: catat waktu, `request_id`, tenant, dan tindakan; bila rahasia mungkin bocor, rotasi dulu, selidiki kemudian.
