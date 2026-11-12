// Package migrate applies the SQL files of migrations/ to a database. It uses the same bookkeeping table as
// golang-migrate (schema_migrations: version, dirty), so `make migrate-up` and this package can be mixed.
//
// Each file runs in its own transaction, together with the update of schema_migrations, and under an advisory
// lock, so two deploys that start at the same moment cannot apply the same file twice.
package migrate

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"

	"gorm.io/gorm"
)

// lockID is the PostgreSQL advisory lock that serializes migration runs.
const lockID int64 = 7_351_003

var fileRe = regexp.MustCompile(`^(\d+)_([a-z0-9_]+)\.up\.sql$`)

// Migration is one up file.
type Migration struct {
	Version int64
	Name    string
	Path    string
}

// Load lists the up files of dir in version order. Two files with the same version are an error.
func Load(dir string) ([]Migration, error) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil, fmt.Errorf("migrate: read %s: %w", dir, err)
	}
	var out []Migration
	seen := map[int64]string{}
	for _, e := range entries {
		m := fileRe.FindStringSubmatch(e.Name())
		if m == nil {
			continue
		}
		v, err := strconv.ParseInt(m[1], 10, 64)
		if err != nil || v < 1 {
			return nil, fmt.Errorf("migrate: bad version in %s", e.Name())
		}
		if other, dup := seen[v]; dup {
			return nil, fmt.Errorf("migrate: %s and %s have the same version %d", other, e.Name(), v)
		}
		seen[v] = e.Name()
		out = append(out, Migration{Version: v, Name: m[2], Path: filepath.Join(dir, e.Name())})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Version < out[j].Version })
	return out, nil
}

// State is what the database says about its own migrations.
type State struct {
	Version  int64 // 0: nothing applied
	Dirty    bool  // a migration failed half way (golang-migrate marks it; this package never leaves it dirty)
	HasTable bool  // schema_migrations exists
	HasData  bool  // other tables exist although schema_migrations is empty or missing: applied by hand
}

const createTable = `CREATE TABLE IF NOT EXISTS schema_migrations (version bigint NOT NULL PRIMARY KEY, dirty boolean NOT NULL)`

// ensureTable creates schema_migrations under the lock: CREATE TABLE IF NOT EXISTS alone races with itself when
// two processes start on an empty database.
func ensureTable(ctx context.Context, db *gorm.DB) error {
	err := db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		if err := tx.Exec("SELECT pg_advisory_xact_lock(?)", lockID).Error; err != nil {
			return err
		}
		return tx.Exec(createTable).Error
	})
	if err != nil {
		return fmt.Errorf("migrate: %w", err)
	}
	return nil
}

// Current reads the state of the database without changing it.
func Current(ctx context.Context, db *gorm.DB) (State, error) {
	var st State
	if err := db.WithContext(ctx).Raw("SELECT to_regclass('schema_migrations') IS NOT NULL").Scan(&st.HasTable).Error; err != nil {
		return st, fmt.Errorf("migrate: %w", err)
	}
	if st.HasTable {
		var rows []struct {
			Version int64
			Dirty   bool
		}
		if err := db.WithContext(ctx).Raw("SELECT version, dirty FROM schema_migrations").Scan(&rows).Error; err != nil {
			return st, fmt.Errorf("migrate: %w", err)
		}
		if len(rows) > 1 {
			return st, errors.New("migrate: schema_migrations has more than one row")
		}
		if len(rows) == 1 {
			st.Version, st.Dirty = rows[0].Version, rows[0].Dirty
		}
	}
	if st.Version == 0 {
		var n int
		err := db.WithContext(ctx).Raw(`SELECT count(*) FROM pg_tables WHERE schemaname = current_schema() AND tablename <> 'schema_migrations'`).Scan(&n).Error
		if err != nil {
			return st, fmt.Errorf("migrate: %w", err)
		}
		st.HasData = n > 0
	}
	return st, nil
}

// Pending returns the migrations that are newer than the state.
func Pending(all []Migration, st State) []Migration {
	var out []Migration
	for _, m := range all {
		if m.Version > st.Version {
			out = append(out, m)
		}
	}
	return out
}

// Up applies every pending migration and returns their versions. It refuses to run on a database that has
// tables but no migration history (applied by hand): record where it is with Baseline first.
func Up(ctx context.Context, db *gorm.DB, dir string) ([]int64, error) {
	all, err := Load(dir)
	if err != nil {
		return nil, err
	}
	if err := ensureTable(ctx, db); err != nil {
		return nil, err
	}
	st, err := Current(ctx, db)
	if err != nil {
		return nil, err
	}
	if st.Dirty {
		return nil, fmt.Errorf("migrate: the database is dirty at version %d (a migration failed half way): fix it by hand, then set dirty to false", st.Version)
	}
	if st.Version == 0 && st.HasData {
		return nil, errors.New("migrate: the database has tables but no migration history (migrations were applied by hand): " +
			"run `tenantctl migrate baseline --version N` with the number of the last migration that was applied")
	}

	var applied []int64
	for _, m := range Pending(all, st) {
		ok, err := applyOne(ctx, db, m)
		if err != nil {
			return applied, err
		}
		if ok {
			applied = append(applied, m.Version)
		}
	}
	return applied, nil
}

// applyOne applies a single file in its own transaction. false means another process did it first.
func applyOne(ctx context.Context, db *gorm.DB, m Migration) (bool, error) {
	sqlText, err := os.ReadFile(m.Path)
	if err != nil {
		return false, fmt.Errorf("migrate: %w", err)
	}
	done := false
	err = db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		if err := tx.Exec("SELECT pg_advisory_xact_lock(?)", lockID).Error; err != nil {
			return err
		}
		// the state is read again under the lock: a concurrent run may have applied this file already
		var cur int64
		if err := tx.Raw("SELECT coalesce(max(version), 0) FROM schema_migrations").Scan(&cur).Error; err != nil {
			return err
		}
		if cur >= m.Version {
			return nil
		}
		if err := tx.Exec(string(sqlText)).Error; err != nil {
			return err
		}
		if err := tx.Exec("DELETE FROM schema_migrations").Error; err != nil {
			return err
		}
		if err := tx.Exec("INSERT INTO schema_migrations (version, dirty) VALUES (?, false)", m.Version).Error; err != nil {
			return err
		}
		done = true
		return nil
	})
	if err != nil {
		return false, fmt.Errorf("migrate: %06d_%s: %w", m.Version, m.Name, err)
	}
	return done, nil
}

// Baseline records that the database is at version without running anything. Use it once for a database whose
// migrations were applied by hand (psql). It refuses to move an existing history.
func Baseline(ctx context.Context, db *gorm.DB, dir string, version int64) error {
	all, err := Load(dir)
	if err != nil {
		return err
	}
	known := false
	for _, m := range all {
		known = known || m.Version == version
	}
	if !known {
		return fmt.Errorf("migrate: there is no migration with version %d in %s", version, dir)
	}
	if err := ensureTable(ctx, db); err != nil {
		return err
	}
	st, err := Current(ctx, db)
	if err != nil {
		return err
	}
	if st.Version != 0 {
		return fmt.Errorf("migrate: the database already has a history (version %d); baseline is only for a database without one", st.Version)
	}
	if err := db.WithContext(ctx).Exec("INSERT INTO schema_migrations (version, dirty) VALUES (?, false)", version).Error; err != nil {
		return fmt.Errorf("migrate: %w", err)
	}
	return nil
}

// Latest returns the highest version in dir (0 when there are none).
func Latest(all []Migration) int64 {
	if len(all) == 0 {
		return 0
	}
	return all[len(all)-1].Version
}
