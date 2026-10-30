# Rahasia dan file env

File env nyata (`.env`, `.env.development`, `.env.production`, `.env.testing`, `.env.local`, `.envrc`) adalah milik tiap developer/server dan tidak masuk repo.

## Asisten AI dilarang

- Membaca, membuat, menyalin, atau mengubah file env nyata. Termasuk lewat `cat`, `grep`, `echo >`, `cp`, `sed -i`.
- Menampilkan nilai rahasia di jawaban, log, test, atau dokumentasi.
- `git add -f` pada file yang diabaikan, dan `git commit --no-verify`.

## Yang boleh

- Mengubah `*.example` (hanya nama variabel dan nilai contoh yang aman).
- Menambah variabel baru: tambahkan ke kedua file `.example`, ke `internal/config/config.go` (+ validasi + test), dan ke `docs/environments.md`.
- Menyuruh developer menjalankan `make env-init ENV=development` untuk membuat file env lokal.

## Production

Rahasia production berasal dari secret manager atau Kubernetes Secret (lihat `deployments/k8s/base/secret.example.yaml`), bukan dari file di repo. Bila rahasia pernah ter-commit, anggap bocor: ganti nilainya, jangan hanya menghapus commit.
