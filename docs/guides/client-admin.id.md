# Panduan Admin Klien

English: [client-admin.md](client-admin.md). File bahasa Inggris adalah sumber utama; perbarui keduanya bersamaan.

Panduan lain: [Operator](operator.id.md) · [Platform Admin](platform-admin.id.md) · [Admin Klien](client-admin.id.md) · [Integrasi API](api-integration.id.md) · [Developer](developer.id.md)

Panduan ini untuk **admin klien** (admin tenant): orang di perusahaan klien yang mengelola pengguna, peran, dan file di tenant mereka. Saat ini belum ada antarmuka web, jadi semua dilakukan lewat API memakai Postman atau curl. Anda tidak perlu menulis kode.

Anda hanya melihat dan mengubah data **tenant Anda sendiri**. Data klien lain tidak pernah terlihat.

## 1. Yang Anda terima dari penyedia layanan

- URL API, mis. `https://api.contoh.com` (di bawah disebut `BASE`)
- Kode tenant, mis. `acme`
- Username dan password sementara admin pertama
- Collection dan environment Postman (opsional tetapi disarankan)

## 2. Menyiapkan Postman

1. Impor collection `GO-GIN-TENANT-TEMPLATE.postman_collection.json` dan environment `GO-GIN-TENANT-TEMPALTE-ENV.postman_environment.json`, (minta kedua file itu ke penyedia layanan), lalu pilih environment tersebut (pojok kanan atas).
2. Isi variabel environment:

| Variabel | Isi |
|---|---|
| `base_url` | `https://api.contoh.com/api/v1` (**tanpa garis miring di akhir**, bila ada garis miring URL menjadi `//` dan hasilnya 404) |
| `tenant_code` | kode tenant Anda |
| `username`, `password` | akun Anda |

1. Jalankan request **Login** di folder Auth. Token tersimpan otomatis dan dipakai request lain. Bila muncul `ECONNREFUSED`, server belum berjalan atau `base_url` salah.

Tanpa Postman (curl): ganti `BASE` dan `TOKEN` pada contoh di bawah.

## 3. Login

```bash
curl -s $BASE/api/v1/auth/login -H 'Content-Type: application/json' -H 'X-Tenant-Code: acme' \
  -d '{"username":"admin","password":"<password>"}'
```

Respons berisi `access_token`, `refresh_token`, `expires_in` (detik). Kirim `Authorization: Bearer <access_token>` pada setiap request berikutnya.

- Access token berlaku singkat (mis. 15 menit). Setelah habis, ambil token baru dengan `POST /api/v1/auth/refresh` body `{"refresh_token":"..."}`. Setiap refresh token hanya bisa dipakai **sekali**; simpan yang baru dari respons.
- Keluar: `POST /api/v1/auth/logout` body `{"refresh_token":"..."}`.
- Cek siapa Anda: `GET /api/v1/auth/me`.
- **Ganti password** (lakukan pada login pertama): `POST /api/v1/auth/password/change` body `{"current_password":"...","new_password":"..."}`. Password baru minimal 10 karakter. Semua sesi Anda berakhir, login ulang.
- **Lupa password**: `POST /api/v1/auth/password/forgot` body `{"tenant_code":"acme","identifier":"<username atau email>"}`. Jawabannya selalu 202 (agar orang luar tidak bisa menebak akun). Bila akun ada, email berisi tautan berlaku sekitar 30 menit; token dari tautan dipakai di `POST /api/v1/auth/password/reset` body `{"token":"...","new_password":"..."}`.
- Login gagal 5 kali dalam 15 menit membuat login ditolak sementara (429, lihat header `Retry-After`). Tunggu, jangan terus mencoba.

## 4. Mengelola pengguna

Akses ke rute ini bergantung pada grup Anda; bila mendapat 403, minta penyedia layanan memberi izin.

| Aksi | Request |
|---|---|
| Daftar pengguna | `GET /api/v1/users?page=1&per_page=20&name=budi&is_active=true` (filter: `username`, `email`, `name`, `is_active`, `group_id`; urut: `sort`; rentang tanggal `from`, `to`) |
| Lihat satu | `GET /api/v1/users/:id` |
| Buat | `POST /api/v1/users` |
| Ubah | `PATCH /api/v1/users/:id` |
| Nonaktifkan/aktifkan | `PATCH /api/v1/users/:id` body `{"is_active": false}` |
| Hapus | `DELETE /api/v1/users/:id` |
| Atur password pengguna | `POST /api/v1/users/:id/password` body `{"password":"..."}` |

Contoh membuat pengguna:

```json
{ "username": "budi", "email": "budi@acme.com", "name": "Budi", "password": "minimal-10-karakter", "group_id": "<id grup>", "is_active": true }
```

Aturan penting:

- `username` dan `email` unik per tenant, huruf besar/kecil dianggap sama. Duplikat = 409.
- `group_id` harus grup di tenant Anda, jika tidak 422. Untuk melepas pengguna dari grup kirim `"group_id": ""` pada PATCH.
- Menonaktifkan, menghapus, atau mengatur password pengguna **mengakhiri semua sesinya** (ia harus login ulang).
- Anda **tidak dapat menonaktifkan atau menghapus akun Anda sendiri** (403), supaya tenant tidak kehilangan admin terakhir.
- Pengguna tanpa grup tidak punya izin apa pun.
- Password tidak pernah ditampilkan oleh sistem. Sampaikan password awal lewat kanal aman dan minta pengguna menggantinya.

## 5. Impor pengguna dari Excel

Untuk membuat banyak pengguna sekaligus.

1. Unduh templat: `GET /api/v1/users/import/template` (file `users-template.xlsx`, ada sheet Help).
2. Isi sheet pertama. Baris 1 adalah nama kolom:

| Kolom | Wajib | Aturan |
|---|---|---|
| `username` | ya | 3 sampai 64 karakter, unik di tenant |
| `email` | ya | alamat email biasa, unik di tenant |
| `name` | ya | maksimal 100 karakter |
| `password` | ya | minimal 10 karakter |
| `group` | tidak | **nama** grup yang sudah ada; kosong = tanpa grup |
| `is_active` | tidak | `yes`/`no` (juga `true`/`false`, `1`/`0`); kosong = yes |

1. **Periksa dulu** tanpa membuat apa pun: `POST /api/v1/users/import?dry_run=true`, body form-data, field `file` bertipe File (di Postman ubah tipe field menjadi **File**).
2. Bila bersih, kirim lagi tanpa `dry_run`. Hasil: `{"rows":120,"created":120,"dry_run":false}`.

Aturan: **semua atau tidak sama sekali**. Bila satu baris bermasalah, respons 422 dan **tidak ada** pengguna yang dibuat. Respons mendaftar masalah per baris, mis. `row 3 password` aturan `min`, `row 5 group` aturan `unknown`. Nomor baris = nomor baris di spreadsheet (judul = baris 1). Arti aturan: `required` (kosong), `min`/`max` (terlalu pendek/panjang), `format` (bentuk salah), `duplicate` (baris kembar di file), `taken` (sudah ada pengguna), `unknown` (grup tidak ada). Perbaiki semuanya lalu kirim ulang.

Batas: file 5 MiB, 500 pengguna per file (pisahkan bila lebih), hanya `.xlsx` (sheet pertama), rumus tidak dihitung. Password ada di file sebagai teks biasa: **hapus file setelah impor**.

## 6. Grup dan izin

Grup adalah peran (mis. `admin`, `kasir`). Izin diberikan ke **grup**, bukan ke pengguna. Aturan dasarnya **ditolak kecuali diizinkan**.

| Aksi | Request |
|---|---|
| Daftar/buat/ubah/hapus grup | `/api/v1/groups` (field `name`, `description`) |
| Lihat izin yang tersedia | `GET /api/v1/rbac/endpoints` |
| Lihat izin sebuah grup | `GET /api/v1/groups/:id/permissions` |
| Ganti seluruh izin grup | `PUT /api/v1/groups/:id/permissions` body `{"endpoint_ids":["<uuid>",...]}` |

Catatan:

- `PUT` **mengganti seluruh daftar**. Kirim daftar lengkap yang diinginkan; `[]` mencabut semua izin.
- Id yang tidak dikenal membuat respons 422 dan tidak ada yang berubah.
- Perubahan berlaku dalam hitungan detik (cache sampai 30 detik pada beberapa kasus).
- Grup yang masih punya pengguna tidak bisa dihapus (409 `conflict`). Pindahkan atau hapus penggunanya dulu.
- Hati-hati dengan grup Anda sendiri: jangan mencabut izin untuk mengelola izin dari grup yang Anda pakai.
- Fitur baru dari rilis berikutnya tidak otomatis diberikan ke grup; penyedia layanan akan memberi tahu atau Anda memberinya lewat `PUT` di atas.

## 7. File dan lampiran

Unggah gambar atau PDF dan lampirkan ke data lain.

| Aksi | Request |
|---|---|
| Unggah | `POST /api/v1/files` (form-data: `file`, opsional `purpose`, mis. `avatar`) |
| Daftar | `GET /api/v1/files` (filter `purpose`, `content_type`, `uploaded_by`, `name`; urut `created_at`, `name`, `size`) |
| Metadata | `GET /api/v1/files/:id` |
| Isi file | `GET /api/v1/files/:id/content` (`?download=1` untuk mengunduh) |
| Hapus | `DELETE /api/v1/files/:id` |

```bash
curl -H "Authorization: Bearer $TOKEN" -F "file=@foto.png" -F "purpose=avatar" $BASE/api/v1/files
```

- Tipe yang diterima: PNG, JPEG, GIF, WebP, PDF. Tipe dicek dari **isi** file, bukan nama; HTML bernama `.png` ditolak (422). SVG tidak diterima.
- Ukuran maksimum default 10 MB (413 bila lebih). File kosong ditolak.
- File klien lain tidak bisa dibuka (404).

**Lampiran** (modul yang mendukung, contoh `activities`): (1) unggah file dan catat `id` dari respons; (2) `POST /api/v1/activities/<id_activity>/documents` body `{"file_id":"<id file>"}`; (3) `GET .../documents` untuk melihat; (4) `DELETE .../documents/<id lampiran>` menghapus lampiran **dan** filenya. `<id_activity>` (di Postman `parent_id`) adalah id record induk yang ingin diberi lampiran, bukan id file.

## 8. Kode kesalahan umum

| HTTP / `error.code` | Arti | Yang dilakukan |
|---|---|---|
| 400 `invalid_request` | Body/parameter salah bentuk | Periksa JSON dan nama parameter |
| 401 `invalid_credentials` | Tenant, username, atau password salah (sengaja tidak dirinci) | Periksa ketiganya |
| 401 `unauthorized` | Token hilang, salah, atau kedaluwarsa | Login atau refresh lagi |
| 403 `forbidden` | Grup Anda tidak punya izin | Minta izin ke admin/penyedia layanan |
| 404 `not_found` | Data tidak ada (atau milik tenant lain) | Periksa id |
| 409 `conflict` | Data sudah ada (username/email) | Gunakan nilai lain |
| 413 `payload_too_large` | Terlalu besar | Kecilkan file/body |
| 422 `validation_failed` | Ada field tidak valid; lihat `details` (`field` dan `rule`) | Perbaiki field tersebut |
| 429 `rate_limited` | Terlalu banyak percobaan login | Tunggu sesuai `Retry-After` |
| 5xx | Masalah di server | Catat header `X-Request-ID` dari respons dan kirim ke penyedia layanan |

Contoh respons galat:

```json
{"success": false, "code": 422, "error": {"code": "validation_failed", "message": "One or more fields are not valid.", "details": [{"field": "name", "rule": "required"}]}}
```

## 9. Praktik keamanan untuk admin klien

- Satu akun per orang; jangan berbagi password.
- Segera ganti password awal; gunakan password manager.
- Nonaktifkan akun orang yang keluar dari perusahaan di hari yang sama.
- Berikan izin seperlunya (prinsip hak akses minimum); jangan jadikan semua orang admin.
- Jangan simpan token atau password di chat, email, atau spreadsheet bersama.
- Laporkan aktivitas mencurigakan ke penyedia layanan, sertakan waktu dan `X-Request-ID`.
