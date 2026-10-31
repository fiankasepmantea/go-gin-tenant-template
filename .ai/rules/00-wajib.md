# Aturan wajib

- Ikuti panduan resmi Go (https://go.dev/doc/), `gofmt`, `go vet`. Layout: `docs/architecture.md` (go.dev/doc/modules/layout, proyek server: kode di `internal/`, binary di `cmd/`).
- Organisasi per fitur di `internal/modules/<nama>`. Dilarang membuat `pkg/`, `utils`, `common`, `helpers`.
- Arah dependensi satu arah: `transport -> modules (handler -> service -> repository) -> platform/core`. `platform` tidak boleh mengimpor `modules`.
- Query tabel ber-tenant **wajib** memakai `tenant_id` dari context (`tenant.IDFrom(ctx)`). Jangan dari body, query, atau header setelah login.
- Primary key: `idgen.New()` (UUID v7, berurutan). Refresh token: `idgen.NewOpaqueToken()`.
- `context.Context` argumen pertama. Error dibungkus `%w`, dibandingkan dengan `errors.Is/As`.
- Log pakai `slog` key-value. Jangan mencatat password, token, atau `PASETO_KEY`.
- Dependency baru: baris `go` di go.mod-nya harus <= 1.26.0, lalu catat di `docs/libraries.md`.
- Test table-driven. Perilaku baru = test baru. `make test-race` harus hijau.
- Tanpa `log.Fatal`/`panic` di luar `main`; tanpa `init()` berefek samping.

- Perubahan perilaku atau struktur wajib memperbarui dokumentasinya di PR yang sama: centang item di `.ai/plans/`, ubah skill di `.ai/skills/` bila polanya berubah, tambah ADR di `docs/adr/` untuk keputusan arsitektur.
- Tabel ber-tenant: model implement `SetTenantID` dan memakai `core.Repository`; jangan menulis `db.Where("tenant_id = ?")` sendiri. Query khusus lewat `repo.Conn(ctx)`.
- Alur request yang menyentuh tabel ber-tenant dibungkus `database.InTenantTx` (mengisi variabel RLS dari token). Error di dalam transaksi membatalkan transaksinya.
- Test yang butuh PostgreSQL: `test/integration` dengan build tag `integration` (`make test-integration`).

## Dilarang

- Menambah dependency yang butuh Go > 1.26.0 tanpa persetujuan pemilik repo.
- Meng-commit `go.work`, hasil build, atau file rahasia.
- Mengubah migration yang sudah pernah diterapkan. Buat migration baru.
