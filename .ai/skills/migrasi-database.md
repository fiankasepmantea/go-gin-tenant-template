# Skill: migration database

- Alat: golang-migrate. Buat: `make migrate-new name=create_xxx`. Terapkan: `make migrate-up DATABASE_URL=...`.
- Satu migration = satu perubahan logis; selalu sediakan `up` dan `down`.
- Jangan edit migration yang sudah dipakai di lingkungan mana pun; buat yang baru.
- Tabel baru ber-tenant: salin pola kolom standar dari `.ai/skills/buat-modul.md`, aktifkan RLS.
- Perubahan berisiko (drop kolom, ubah tipe): pecah jadi beberapa rilis (tambah, migrasi data, baru hapus).
- Jangan menaruh data rahasia (password, kunci) di file migration.
