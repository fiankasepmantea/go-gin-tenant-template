# Panduan Platform Admin

English: [platform-admin.md](platform-admin.md). File bahasa Inggris adalah sumber utama; perbarui keduanya bersamaan.

Panduan lain: [Operator](operator.id.md) · [Platform Admin](platform-admin.id.md) · [Admin Klien](client-admin.id.md) · [Integrasi API](api-integration.id.md) · [Developer](developer.id.md)

Panduan ini untuk **platform admin / pemilik produk**: orang yang mengelola seluruh platform, mendaftarkan tenant (klien), dan merilis versi aplikasi mobile. Belum ada antarmuka web, jadi semua dilakukan lewat CLI `tenantctl` (di server) dan API (curl atau Postman).

## 1. Dua jenis akun

|  | Platform admin | Admin/pengguna tenant |
|---|---|---|
| Milik | Seluruh platform, tanpa tenant | Satu tenant (klien) |
| Dibuat dengan | `tenantctl admin create` | `tenantctl user create --tenant ...` atau API `/users` |
| Login | `POST /api/v1/auth/platform/login` | `POST /api/v1/auth/login` + kode tenant |
| Bisa memanggil | Hanya rute berscope **platform** (mis. App Versions) | Rute berscope **tenant** sesuai izin grupnya |
| Melihat data tenant? | **Tidak**. Token platform di rute tenant mendapat 403 | Hanya data tenant sendiri |

Platform admin sengaja tidak bisa membuka data klien lewat API. Jangan mencoba "login sebagai klien"; bila perlu bantuan di data klien, minta admin klien atau buat akun khusus di tenant tersebut atas izin klien.

## 2. Membuat platform admin

Tidak ada pendaftaran publik. Jalankan di mesin yang terhubung ke database (lihat Panduan Operator). Password dibaca dari stdin supaya tidak tersimpan di riwayat shell; minimal 10 karakter.

```bash
read -rsp "Password: " PW; echo
printf '%s\n' "$PW" | tenantctl admin create \
  --username root --email root@example.com --name "Platform Admin" --password-stdin
unset PW
```

Catatan: `tenantctl` = `go run ./cmd/cli` (atau hasil `go build -o tenantctl ./cmd/cli`).

Login:

```bash
curl -s $BASE/api/v1/auth/platform/login -H 'Content-Type: application/json' \
  -d '{"username":"root","password":"<password>"}'
```

Respons: `access_token` (berlaku `ACCESS_TOKEN_TTL`), `refresh_token`, `expires_in`. Pakai sebagai `Authorization: Bearer <access_token>`. Di Postman, isi variabel environment `platform_username` dan `platform_password`, lalu jalankan request Login platform di collection.

Praktik baik: satu akun per orang (bukan akun bersama), jumlah platform admin sedikit, dan password dikelola di password manager.

Lupa password platform admin: `POST /api/v1/auth/platform/password/forgot` dengan `{"identifier": "<username atau email>"}` (butuh SMTP aktif), lalu `POST /api/v1/auth/password/reset`.

## 3. Onboarding tenant baru (klien)

Semua langkah di bawah dijalankan sekali per klien.

1. **(Opsional) Database khusus**. Hanya untuk tenant baru. Set `TENANT_DB_URL_<REF>` di environment server, jalankan `tenantctl migrate up --ref <REF>`, dan gunakan `--db-ref <REF>` pada langkah 2. Tenant yang sudah ada **tidak** bisa dipindah ke database khusus.
2. **Buat tenant**:
  ```bash
  tenantctl tenant create --code acme --name "Acme Inc"
  ```
  `--code` dipakai klien saat login (huruf kecil, singkat, tetap; jangan diubah-ubah karena ikut menjadi subdomain bila `TENANT_BASE_DOMAIN` dipakai).
3. **Sinkronkan endpoint** bila ada rilis baru: `tenantctl endpoint sync`.
4. **Buat grup admin** dan beri izin:
  ```bash
  tenantctl group create --tenant acme --name admin --description "Full access"
  tenantctl group grant  --tenant acme --group admin --all
  ```
  `--all` memberi semua endpoint **berscope tenant**. Untuk peran terbatas buat grup lain dan beri endpoint tertentu, mis. `--endpoint "GET /api/v1/users"` (bisa diulang).
5. **Buat admin pertama klien** dan masukkan ke grup:
  ```bash
  read -rsp "Password: " PW; echo
  printf '%s\n' "$PW" | tenantctl user create --tenant acme --username admin \
    --email admin@acme.com --name "Acme Admin" --group admin --password-stdin
  unset PW
  ```
6. **Uji login** sebagai admin klien:
  ```bash
  curl -s $BASE/api/v1/auth/login -H 'Content-Type: application/json' -H 'X-Tenant-Code: acme' \
    -d '{"username":"admin","password":"<password>"}'
  ```
7. **Serahkan ke klien** (lewat kanal aman, bukan email biasa): URL API, kode tenant, username, password sementara, dan Panduan Admin Klien + Panduan Integrasi API. Minta klien segera mengganti password (`POST /api/v1/auth/password/change`).

Setelah itu admin klien membuat pengguna lain sendiri lewat API (lihat Panduan Admin Klien). Dengan `tenantctl user set-group` Anda dapat memindahkan pengguna yang sudah ada ke grup.

Checklist onboarding: tenant dibuat, grup admin + izin, admin pertama dalam grup, login berhasil, kredensial diserahkan, password diganti, klien tahu cara menghubungi dukungan.

## 4. Versi aplikasi mobile (App Versions)

Fitur ini milik platform admin; versi bersifat global (bukan per tenant). Perangkat klien memeriksa versi lewat rute publik tanpa token.

| Aksi | Rute | Siapa |
|---|---|---|
| Daftar, buat, lihat, ubah, hapus versi | `/api/v1/app-versions` | Token platform admin saja |
| Unggah APK | `PUT /api/v1/app-versions/:id/package` | Token platform admin saja |
| Versi terbaru, cek update, unduh APK | `/api/v1/public/app-versions/...` | Siapa saja, tanpa token |

**Merilis versi baru**

1. Buat catatan versi:
  ```bash
  curl -s -X POST $BASE/api/v1/app-versions -H "Authorization: Bearer $TOKEN" -H 'Content-Type: application/json' -d '{
    "platform": "android", "version": "1.10.0", "min_supported_version": "1.5.0",
    "is_force_update": false, "release_notes": "Perbaikan bug", "store_url": "https://play.google.com/..." }'
  ```
  - Versi: 1 sampai 4 angka dipisah titik (`1.4`, `2.0.1`), tanpa huruf atau awalan `v`; dibandingkan sebagai angka (`1.10.0` lebih baru dari `1.9.0`); `1.10` dan `1.10.0` dianggap sama (duplikat = 409).
  - `platform` (`android`, `ios`, `web`) dan `version` tidak bisa diubah setelah dibuat.
  - `min_supported_version` (default `0.0.0`) tidak boleh di atas `version`; `store_url` harus http/https.
2. **Unggah APK** (khusus Android), field multipart `file`:
  ```bash
  curl -s -X PUT $BASE/api/v1/app-versions/<id>/package -H "Authorization: Bearer $TOKEN" -F file=@app-release.apk
  ```
  File harus arsip zip yang berisi `AndroidManifest.xml`; ukuran maks `UPLOAD_APK_MAX_MB` (default 200, lebih besar = 413). Server mencatat ukuran dan SHA-256. Mengunggah lagi mengganti paket lama. Server **tidak** memeriksa tanda tangan APK (Android yang memeriksa saat instal).
3. **Uji sebagai perangkat** (tanpa token):
  ```bash
  curl -s "$BASE/api/v1/public/app-versions/check?platform=android&version=1.9.0"
  ```
  Hasil: `update_available`, `force_update`, `download_url`.

**Update paksa**: `force_update` bernilai true bila versi terpasang di bawah `min_supported_version` rilis terbaru, atau rilis terbaru memiliki `is_force_update=true` dan perangkat tertinggal. Perangkat yang sudah di versi terbaru tidak pernah dipaksa. Aplikasi mobile bertanggung jawab menampilkan layar update.

Unduhan memakai `Content-Type: application/vnd.android.package-archive`, `ETag` (SHA-256), dan mendukung `Range` (lanjut unduh) serta `If-None-Match` (304). Rute publik tidak ber-rate-limit di aplikasi: pasang CDN/proxy dan cache unduhan. Unduhan bersifat publik; jangan unggah APK yang harus rahasia. Menghapus versi menghapus paketnya.

## 5. Operasional harian

- **Rilis baru backend**: setelah deploy, `endpoint sync` lalu `group grant` untuk grup yang berhak atas fitur baru, di setiap tenant (tanpa itu pengguna mendapat 403).
- **Menonaktifkan pengguna klien**: dilakukan admin klien (`PATCH /users/:id` dengan `is_active=false`), yang juga mengakhiri sesinya. Bila admin klien tidak tersedia, hubungi operator.
- **Reset password admin klien**: admin klien lain dapat memakai `POST /users/:id/password`; bila tidak ada, pengguna memakai alur lupa password (`/auth/password/forgot`), atau operator membuat admin baru dengan `tenantctl user create`.
- **Offboarding klien**: gunakan perintah `tenantctl` tenant suspend (hentikan akses, data tetap), tenant export (arsip zip data dan file untuk klien), lalu setelah masa simpan sesuai kontrak, tenant delete dengan --confirm berisi kode tenant (permanen, hanya untuk tenant yang sudah di-suspend). Database khusus tenant tidak ikut di-drop dan backup harus dibersihkan sendiri. Rincian: docs/offboarding.md.
- **Kebocoran kredensial**: ganti password akun terkait (sesi berakhir); bila `PASETO_KEY` bocor, ikuti langkah rotasi di Panduan Operator.

## 6. Pemecahan masalah

| Gejala | Penyebab | Tindakan |
|---|---|---|
| 401 `invalid_credentials` saat login platform | Username/password salah, atau memakai akun tenant di rute platform | Gunakan `/auth/platform/login` dengan akun platform |
| 403 di `/app-versions` | Token tenant dipakai | Gunakan token platform admin |
| 403 di rute tenant dengan token platform | Memang tidak diizinkan | Gunakan akun tenant |
| 429 `rate_limited` | Terlalu banyak login gagal | Tunggu sesuai `Retry-After` |
| 409 saat membuat versi | Versi sama sudah ada | Naikkan nomor versi |
| 413 saat unggah APK | Melebihi `UPLOAD_APK_MAX_MB` atau batas proxy | Naikkan batas di server dan proxy |
| 422 saat unggah APK | Bukan zip berisi `AndroidManifest.xml` | Unggah APK asli |
| Pengguna klien 403 di fitur baru | Rute belum diberi izin | `endpoint sync` + `group grant` |
