# Lampiran: file untuk record modul lain

Pola untuk "banyak foto/dokumen milik satu record": foto kegiatan, dokumen kasus, lampiran laporan. File diunggah
lewat `/files` (lihat `docs/files.md`); tabel lampiran hanya menyimpan **`file_id`**, bukan path.

## Alur

1. `POST /api/v1/files` (multipart, field `file`) -> respons berisi `file_id`.
2. `POST /api/v1/kegiatan/:id/dokumentasi` dengan `{"file_id": "..."}` -> file terlampir.
3. `GET /api/v1/kegiatan/:id/dokumentasi` -> daftar lampiran, masing-masing dengan `name`, `content_type`,
   `size`, dan `url` (isi file diambil dari `GET /api/v1/files/<file_id>/content`).
4. `DELETE /api/v1/kegiatan/:id/dokumentasi/:attachment_id` -> baris lampiran, file, dan isinya terhapus.

## Memasang untuk tabel Anda (contoh: `dokumentasi_kegiatan`)

**1. Migrasi** (`tenantctl make:migration add_dokumentasi_kegiatan`). Tabel induk (`kegiatan`) harus punya
`UNIQUE (tenant_id, kegiatan_id)`:

```sql
CREATE TABLE dokumentasi_kegiatan (
    dokumentasi_kegiatan_id uuid        PRIMARY KEY,
    tenant_id               uuid        NOT NULL,
    kegiatan_id             uuid        NOT NULL,
    file_id                 uuid        NOT NULL,
    created_at              timestamptz NOT NULL DEFAULT now(),
    FOREIGN KEY (tenant_id, kegiatan_id) REFERENCES kegiatan (tenant_id, kegiatan_id) ON DELETE CASCADE,
    FOREIGN KEY (tenant_id, file_id)     REFERENCES files (tenant_id, file_id),
    UNIQUE (file_id)                                    -- satu file hanya untuk satu lampiran
);
CREATE INDEX dokumentasi_kegiatan_kegiatan_idx ON dokumentasi_kegiatan (tenant_id, kegiatan_id);

ALTER TABLE dokumentasi_kegiatan ENABLE ROW LEVEL SECURITY;
ALTER TABLE dokumentasi_kegiatan FORCE ROW LEVEL SECURITY;
CREATE POLICY tenant_isolation ON dokumentasi_kegiatan
    USING (tenant_id = app_tenant()) WITH CHECK (tenant_id = app_tenant());
```

**2. Daftarkan handler** di `internal/app/app.go`, di atas baris `// tenantctl:modules`:

```go
attachment.NewHandler(attachment.Config{
    Name: "dokumentasi", Parent: "kegiatan",
    Table: "dokumentasi_kegiatan", IDColumn: "dokumentasi_kegiatan_id",
    ParentTable: "kegiatan", ParentColumn: "kegiatan_id",
}, d.DB, file.NewRepository(d.DB), d.Storage, chain...),
```

Untuk tabel kedua (`dokumentasi_gangguan_kamtibmas`) ulangi dengan nama tabel dan kolom yang lain. Lalu
`endpoint sync` dan `group grant`.

## Mengapa foreign key-nya berpasangan `(tenant_id, ...)`

Pengecekan foreign key di PostgreSQL tidak tunduk pada RLS. Dengan FK biasa (`kegiatan_id` saja), tenant B yang
menebak id kegiatan milik tenant A bisa melampirkan fotonya ke sana. FK berpasangan membuat database sendiri
menolaknya. Aplikasi memeriksa juga (404 untuk record tenant lain, 422 untuk file tenant lain), jadi dua lapis.

## Catatan

- Nama tabel dan kolom di `Config` masuk ke SQL, jadi hanya huruf kecil, angka, dan `_` yang diterima
  (selain itu handler berhenti saat server start). Nilainya datang dari kode, tidak pernah dari request.
- Menghapus record induk ikut menghapus baris lampiran (`ON DELETE CASCADE`), tetapi **tidak** file dan isinya.
  Hapus lampirannya dulu lewat endpoint `DELETE`, atau tulis pembersihan sendiri di modul induk.
- Daftar lampiran dibatasi `Limit` (default 200) per record.
