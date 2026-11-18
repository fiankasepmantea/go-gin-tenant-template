# Panduan Integrasi API

English: [api-integration.md](api-integration.md). File bahasa Inggris adalah sumber utama; perbarui keduanya bersamaan.

Panduan lain: [Operator](operator.id.md) · [Platform Admin](platform-admin.id.md) · [Admin Klien](client-admin.id.md) · [Integrasi API](api-integration.id.md) · [Developer](developer.id.md)

Panduan ini untuk **developer klien** yang menghubungkan aplikasi (web, mobile, atau layanan lain) ke API. Spesifikasi lengkap dan dapat dicoba ada di `GET /docs` (Swagger UI) dan `GET /openapi.json`, bila diaktifkan operator (`DOCS_ENABLED`). Panduan ini menjelaskan konvensi yang berlaku di semua endpoint.

## 1. Dasar

- Base URL: `https://<host>/api/v1`. Semua request dan respons berformat JSON (`Content-Type: application/json`), kecuali upload (`multipart/form-data`) dan unduhan file.
- Tenant ditentukan **hanya saat login**; sesudahnya tenant diambil dari token.
- Isi body maksimal 1 MiB (endpoint auth: 16 KiB; upload punya batas sendiri, lihat bagian 7). Lebih besar = 413.
- Setiap respons membawa header `X-Request-ID`. Simpan di log aplikasi Anda dan sertakan saat melapor masalah. Anda boleh mengirim `X-Request-ID` sendiri.

## 2. Autentikasi dan siklus token

| Request | Body | Hasil |
|---|---|---|
| `POST /auth/login` | `{"tenant_code"?, "username", "password"}` | token |
| `POST /auth/platform/login` | `{"username", "password"}` | token (khusus platform admin) |
| `POST /auth/refresh` | `{"refresh_token"}` | token baru; refresh token lama dicabut |
| `POST /auth/logout` | `{"refresh_token"}` | 204, idempoten |
| `GET /auth/me` | header bearer | `user_id`, `level`, `tenant_id` |
| `POST /auth/password/forgot` | `{"tenant_code"?, "identifier"}` | 202, selalu sama |
| `POST /auth/password/reset` | `{"token", "new_password"}` | 204 |
| `POST /auth/password/change` | `{"current_password", "new_password"}` + bearer | 204, semua sesi berakhir |

**Menentukan tenant saat login**, berurutan: subdomain (`tokoa.app.com`), header `X-Tenant-Code`, lalu `tenant_code` di body. Aplikasi mobile/API biasanya memakai header.

Respons token (bentuk OAuth 2, tanpa envelope):

```json
{ "access_token": "v4.local...", "token_type": "Bearer", "expires_in": 900, "refresh_token": "<tenant_id>.xxxxxxxx" }
```

Kirim `Authorization: Bearer <access_token>` di setiap request yang butuh login. Respons token memakai `Cache-Control: no-store`.

**Strategi di klien**

1. Simpan access token **di memori**; refresh token di penyimpanan aman (Keychain/Keystore di mobile; cookie `HttpOnly` lewat backend Anda untuk web). Jangan di `localStorage` bila bisa dihindari.
2. Jadwalkan refresh sebelum `expires_in` habis, atau refresh sekali saat menerima 401 lalu ulangi request itu satu kali.
3. Refresh token **sekali pakai**. Selalu simpan pasangan token baru dari respons. Bila dua tab/proses me-refresh bersamaan dengan token yang sama, hanya satu yang menang; serialkan refresh dalam satu antrean.
4. **Reuse detection**: memakai ulang refresh token yang sudah dipakai mencabut **semua** sesi pengguna itu. Anggap 401 `invalid_refresh_token` sebagai "sesi berakhir": arahkan ke layar login, jangan mengulang.
5. Access token tidak dicek ke database setiap request, sehingga setelah logout ia tetap berlaku sampai kedaluwarsa (default 15 menit di production). Jangan menyimpannya lebih lama dari perlu.
6. Login gagal dihitung (default 5 per 15 menit per tenant+username+IP): respons 429 `rate_limited` dengan header `Retry-After` (detik). Hormati header itu.

Semua kegagalan login memberi satu jawaban yang sama, `invalid_credentials` (tenant salah, user salah, password salah, user nonaktif, tenant ditangguhkan), jadi jangan menampilkan perbedaannya.

## 3. Envelope respons

Sukses:

```json
{ "success": true, "code": 200, "data": { }, "message": "opsional" }
```

Galat:

```json
{ "success": false, "code": 422,
  "error": { "code": "validation_failed", "message": "One or more fields are not valid.",
             "details": [{ "field": "name", "rule": "required" }] } }
```

- `code` teratas = status HTTP. **Cabangkan logika Anda pada ****`error.code`**, bukan pada teks `message` (bahasa Inggris dan dapat berubah). `details` hanya ada untuk `validation_failed`.
- 404, 405, dan 500 memakai envelope yang sama.
- Pengecualian bentuk sukses: `/auth/login`, `/auth/platform/login`, `/auth/refresh` (bentuk OAuth di atas).

| `error.code` | HTTP | Arti | Tindakan klien |
|---|---|---|---|
| `invalid_request` | 400 | Body rusak, parameter query tidak dikenal/salah | Perbaiki request, jangan diulang |
| `unauthorized` | 401 | Token hilang/salah/kedaluwarsa | Refresh lalu ulangi sekali |
| `invalid_credentials` | 401 | Login gagal | Tampilkan pesan umum |
| `invalid_refresh_token` | 401 | Refresh token tidak dikenal/kedaluwarsa/sudah dipakai | Ke layar login |
| `forbidden` | 403 | Tidak ada izin (atau token platform di rute tenant, atau sebaliknya) | Tampilkan "tidak berhak" |
| `not_found` | 404 | Tidak ada (termasuk milik tenant lain) | Tampilkan tidak ditemukan |
| `method_not_allowed` | 405 | Metode salah | Perbaiki kode |
| `conflict` | 409 | Sudah ada (unik) | Minta nilai lain |
| `payload_too_large` | 413 | Terlalu besar | Kecilkan |
| `validation_failed` | 422 | Field tidak valid; juga referensi ke data yang tidak ada | Tandai field di `details` |
| `rate_limited` | 429 | Terlalu banyak percobaan | Tunggu `Retry-After` |
| `internal_error` | 500 | Tak terduga | Ulang dengan backoff; laporkan `X-Request-ID` |
| `service_unavailable` | 503 | Database khusus tenant tidak terjangkau | Ulang dengan backoff |

Kode baru dapat ditambahkan di masa depan; kode dan status yang ada tidak akan berubah. Perlakukan kode tak dikenal sesuai status HTTP-nya.

## 4. List: paginasi, filter, urutan

`GET` pada koleksi selalu berpaginasi:

```json
{ "success": true, "code": 200, "data": {
  "total_data": 45, "total_pages": 3, "curr_page": 2, "next_page": 3, "prev_page": 1,
  "data_per_page": 20, "data": [ ] } }
```

`prev_page` tidak ada di halaman pertama; di halaman terakhir `next_page` tetap pada halaman terakhir (bandingkan `curr_page` dengan `total_pages` untuk berhenti).

| Parameter | Contoh | Catatan |
|---|---|---|
| `page`, `per_page` | `?page=2&per_page=50` | Default 1 dan 20; `per_page` maks 100 |
| `sort` | `?sort=-created_at,name` | `-` = menurun; hanya kolom yang diizinkan resource |
| `from`, `to` | `?from=2026-01-01&to=2026-01-31` | Rentang `created_at`, `to` inklusif; tanggal atau RFC 3339 (UTC) |
| `<kolom>` | `?name=admin` | Filter sama dengan; hanya kolom yang diizinkan |

Parameter yang tidak dikenal, parameter yang diulang, dan `;` di query string ditolak dengan 400 (agar salah ketik tidak diam-diam mengembalikan data tanpa filter). Selain itu resource CRUD memiliki `GET /x/show_by/:kolom/:nilai` dan `GET /x/all_by/:kolom/:nilai`.

## 5. Verba dan semantik

- `POST` membuat (201 dengan data baru), `PATCH` mengubah **hanya field yang dikirim** (200), `DELETE` = soft delete (200). `:id` harus UUID, selain itu 404.
- Field tidak dikenal di body diabaikan atau ditolak sesuai resource; jangan mengirim `id`, `tenant_id`, atau timestamp (tidak bisa diatur dari klien).
- Tidak ada kunci idempotensi bawaan. `POST` yang diulang setelah timeout dapat membuat duplikat kecuali ada batas unik (username, email, nama) yang menjawab 409. Rancang klien agar tidak mengulang `POST` otomatis tanpa memeriksa lebih dulu.
- Waktu dalam UTC, format RFC 3339.

## 6. Izin (RBAC)

Setiap endpoint diperiksa terhadap izin grup pengguna (default-deny): 403 bila belum diberikan. Endpoint dikenali dari metode + pola rute (`GET /api/v1/users/:id`). Aplikasi Anda sebaiknya menangani 403 dengan menyembunyikan fitur atau menampilkan pesan, dan operator memberi izin lewat `PUT /groups/:id/permissions`. Rute `/auth/*` dan `/public/*` tidak memerlukan izin grup.

## 7. File dan lampiran

- Unggah: `POST /files`, `multipart/form-data`, field `file` (wajib) dan `purpose` (opsional). Tipe dideteksi dari isi: PNG, JPEG, GIF, WebP, PDF; selain itu 422. Maks `UPLOAD_MAX_MB` (default 10 MB), lebih = 413. Respons berisi `id` file.
- Unduh: `GET /files/:id/content` (`?download=1` memaksa unduhan). Endpoint ini **butuh header Authorization**, jadi tag `<img src>` tidak bisa memakainya langsung: ambil dengan `fetch` lalu tampilkan sebagai blob, atau minta backend Anda mengambilnya.
- Lampiran pada data lain (pola): `POST /<induk>/:id/documents` dengan `{"file_id":"..."}`, `GET` untuk daftar, `DELETE /<induk>/:id/documents/:attachment_id` (menghapus lampiran dan file). `:id` = id record induk, bukan id file.
- Impor pengguna Excel: `GET /users/import/template`, `POST /users/import?dry_run=true`, lalu tanpa `dry_run` (semua atau tidak sama sekali; galat per baris ada di `details`, mis. `row 3 password` aturan `min`). Batas: 5 MiB, 500 baris.

## 8. Endpoint publik untuk aplikasi mobile (cek versi)

Tanpa token, di bawah `/api/v1/public/app-versions`:

| Request | Hasil |
|---|---|
| `GET /latest?platform=android` | Versi terbaru, catatan rilis, `store_url`, ukuran paket dan hash |
| `GET /check?platform=android&version=1.9.0` | `update_available`, `force_update`, `download_url` |
| `GET /latest/download?platform=android` | APK terbaru |
| `GET /:id/download` | APK satu versi |

Alur yang disarankan saat aplikasi dibuka: panggil `check` dengan versi terpasang. Bila `force_update` true, tampilkan layar yang memblokir sampai pengguna memperbarui (`store_url` atau `download_url`); bila hanya `update_available`, tawarkan pembaruan. Unduhan mendukung `Range` (lanjutkan unduhan) dan `ETag`/`If-None-Match` (304). Bandingkan versi sebagai angka (`1.10.0` > `1.9.0`), bukan sebagai teks. Endpoint publik tidak dibatasi laju oleh aplikasi; cache hasilnya di klien.

## 9. Contoh curl lengkap

```bash
BASE=https://api.contoh.com/api/v1

# 1. login
RESP=$(curl -s $BASE/auth/login -H 'Content-Type: application/json' -H 'X-Tenant-Code: acme' \
  -d '{"username":"admin","password":"<password>"}')
TOKEN=$(echo "$RESP" | jq -r .access_token); REFRESH=$(echo "$RESP" | jq -r .refresh_token)

# 2. daftar pengguna, halaman 1, 20 data
curl -s "$BASE/users?page=1&per_page=20&sort=-created_at" -H "Authorization: Bearer $TOKEN"

# 3. buat pengguna
curl -s -X POST $BASE/users -H "Authorization: Bearer $TOKEN" -H 'Content-Type: application/json' \
  -d '{"username":"budi","email":"budi@acme.com","name":"Budi","password":"minimal-10-karakter"}'

# 4. unggah file
curl -s -X POST $BASE/files -H "Authorization: Bearer $TOKEN" -F file=@foto.png -F purpose=avatar

# 5. refresh token (simpan pasangan baru!)
curl -s $BASE/auth/refresh -H 'Content-Type: application/json' -d "{\"refresh_token\":\"$REFRESH\"}"

# 6. logout
curl -s -X POST $BASE/auth/logout -H 'Content-Type: application/json' -d "{\"refresh_token\":\"$REFRESH\"}"
```

## 10. Daftar periksa integrasi

- Cabang logika pada `error.code`, bukan `message`
- Refresh token disimpan aman, refresh diserialkan, pasangan baru selalu disimpan
- 401 memicu satu kali refresh; kegagalan refresh = layar login
- 429 menghormati `Retry-After`
- 403 ditangani (fitur disembunyikan atau pesan jelas)
- Daftar memakai `per_page` <= 100 dan berhenti di `curr_page == total_pages`
- `X-Request-ID` dicatat di log klien
- Tidak ada `POST` otomatis yang diulang tanpa pemeriksaan
- Aplikasi mobile memanggil `check` versi saat dibuka
- Tidak ada token atau password di log, repositori, atau tracker bug
