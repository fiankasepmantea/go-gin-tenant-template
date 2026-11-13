# File (`/api/v1/files`)

Upload, daftar, unduh, dan hapus file milik tenant (avatar, gambar, PDF). Metadata disimpan di tabel `files`
(dengan RLS), isi file di *storage backend*.

| Method | Path | Fungsi |
|---|---|---|
| POST | `/files` | upload, `multipart/form-data`: field `file` (wajib), `purpose` (opsional, mis. `avatar`) |
| GET | `/files` | daftar (filter `purpose`, `content_type`, `uploaded_by`, `name`; sort `created_at`, `name`, `size`) |
| GET | `/files/:id` | metadata satu file |
| GET | `/files/:id/content` | isi file (`?download=1` memaksa unduh, bukan tampil di browser) |
| DELETE | `/files/:id` | hapus (baris dan isi file) |

```bash
curl -H "Authorization: Bearer $TOKEN" -F "file=@foto.png" -F "purpose=avatar" http://localhost:8080/api/v1/files
curl -H "Authorization: Bearer $TOKEN" http://localhost:8080/api/v1/files/<file_id>/content -o foto.png
```

Di Postman: Body -> form-data, ubah tipe field `file` menjadi **File**.

## Aturan

- **Tipe file** dideteksi dari isi file, bukan dari nama atau header dari klien. Diizinkan: PNG, JPEG, GIF, WebP,
  PDF. File HTML yang diberi nama `.png` ditolak (422). SVG sengaja tidak diizinkan karena bisa membawa script.
  Daftar tipe ada di `file.DefaultTypes`.
- **Ukuran** maksimal `UPLOAD_MAX_MB` (default 10 MiB) -> 413 bila lebih. File kosong ditolak (422).
- **Nama file** dari klien dibersihkan (tanpa folder dan karakter kontrol). Isi disimpan dengan kunci buatan
  server, `<tenant_id>/<file_id>`, jadi nama dari klien tidak pernah menjadi path di disk.
- **Isolasi tenant**: metadata dijaga filter tenant dan RLS, isi file ada di bawah folder tenant. File tenant
  lain tidak bisa dibuka (404).
- **Mengunduh** mengirim `X-Content-Type-Options: nosniff` dan `Content-Security-Policy: sandbox`, supaya
  browser tidak pernah menjalankan file unggahan sebagai halaman.
- Akses diatur RBAC seperti endpoint lain. Setelah deploy: `endpoint sync`, lalu `group grant`.
- Endpoint `content` membutuhkan header `Authorization`. Tag `<img src>` tidak bisa mengirimnya: ambil dengan
  `fetch` lalu tampilkan sebagai blob, atau minta file lewat backend Anda.

## Konfigurasi

```dotenv
STORAGE_DRIVER=local          # hanya local yang bawaan
STORAGE_DIR=./storage         # folder isi file (sudah di .gitignore). Di production: volume yang persisten
UPLOAD_MAX_MB=10              # 1 sampai 100
```

Folder `STORAGE_DIR` harus ikut di-backup bersama database, dan dipakai bersama bila server berjalan lebih dari satu
(volume bersama). Jika tidak, gunakan backend S3 (di bawah).

## Mengganti ke S3 (atau MinIO, GCS)

Handler hanya mengenal `storage.Store` (`internal/platform/storage/storage.go`): `Put`, `Open`, `Delete`.
Buat tipe baru yang memenuhi interface itu, misalnya `storage.S3`, lalu pilih di `cmd/server/main.go` menurut
`STORAGE_DRIVER`. Tidak ada kode lain yang berubah. Kunci yang diterima sudah berbentuk path yang aman
(`<tenant_id>/<file_id>`) dan cocok sebagai object key.

## Yang perlu diketahui

- Upload dan unduh berjalan di dalam transaksi database request, jadi satu koneksi terpakai selama file
  ditransfer. Batas ukuran menjaga ini tetap kecil. Untuk file besar, pakai URL bertanda tangan dari S3.
- Jika commit database gagal setelah isi file tersimpan, isi file bisa menjadi yatim (tanpa baris). Ini jarang
  terjadi dan tidak berbahaya: nama filenya acak dan tidak bisa diakses lewat API.
- Tidak ada dukungan `Range` (unduh sebagian). Ditambahkan bila ada kebutuhan streaming video.
