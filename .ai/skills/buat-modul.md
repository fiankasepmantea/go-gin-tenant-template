# Skill: membuat modul baru

Modul = satu fitur utuh di `internal/modules/<nama>/`.

## Langkah

1. Migration dulu: `make migrate-new name=create_<tabel>`. Kolom standar: `<t>_id` (UUID, PK), `<t>_unique`, `tenant_id`, `created_at`, `updated_at`, `deleted_at`. Indeks: `tenant_id` kolom pertama; unique parsial `WHERE deleted_at IS NULL`.
2. File modul (nama paket = nama folder, huruf kecil, tunggal):
   - `model.go` struct GORM dengan tag kolom, `gorm.DeletedAt` untuk soft delete, dan method `TableName`, `PrimaryKey`, `GetID`, `SetID`, plus `SetTenantID` bila ber-tenant. Contoh: `internal/modules/group/model.go`.
   - `repository.go` embed `*core.Repository[T, *T]` (CRUD, pagination, filter tenant otomatis) dan tambah query khusus lewat `repo.Conn(ctx)`. Contoh: `internal/modules/group/repository.go`, `internal/modules/user/repository.go`.
   - `service.go` logika bisnis; menerima interface repository lewat konstruktor.
   - `handler.go` Gin handler tipis: bind, validasi, panggil service, tulis respons.
   - `routes.go` fungsi `Register(r gin.IRouter, h *Handler)`.
   - `*_test.go` per file.
3. Daftarkan route di `internal/transport/rest` dan endpoint-nya di whitelist (modul `endpoint`).
4. Test isolasi tenant di `test/integration` (salin pola `repository_test.go`), lalu `make fmt vet test-race test-integration`.

## Pola

- Handler tidak memanggil GORM. Service tidak mengenal `gin.Context`.
- Interface didefinisikan di sisi pemakai (service), bukan di sisi implementasi.
- Cetakan kode akan tersedia lewat `tenantctl make:module <nama>` (fase generator); sampai itu ada, salin pola dari modul pertama yang selesai.
