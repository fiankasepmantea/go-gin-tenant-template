package core

import (
	"errors"
	"testing"

	"github.com/jackc/pgx/v5/pgconn"
	"gorm.io/gorm"
)

func TestMapErr(t *testing.T) {
	tests := []struct {
		name    string
		in      error
		want    error // errors.Is target; nil = must not match any sentinel
		wantPG  bool  // the driver error must stay reachable
		wantRaw bool
	}{
		{"record not found", gorm.ErrRecordNotFound, ErrNotFound, false, false},
		{"unique violation", &pgconn.PgError{Code: "23505"}, ErrConflict, true, false},
		{"foreign key violation", &pgconn.PgError{Code: "23503"}, ErrReference, true, false},
		{"not null violation", &pgconn.PgError{Code: "23502"}, ErrInvalidInput, true, false},
		{"check violation", &pgconn.PgError{Code: "23514"}, ErrInvalidInput, true, false},
		{"invalid uuid text", &pgconn.PgError{Code: "22P02"}, ErrInvalidInput, true, false},
		{"other database error", &pgconn.PgError{Code: "57014"}, nil, true, true},
		{"plain error", errors.New("boom"), nil, false, true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := mapErr("things", "op", tt.in)
			if tt.want != nil && !errors.Is(got, tt.want) {
				t.Errorf("mapErr = %v, want errors.Is %v", got, tt.want)
			}
			if tt.wantRaw {
				for _, s := range []error{ErrNotFound, ErrConflict, ErrReference, ErrInvalidInput} {
					if errors.Is(got, s) {
						t.Errorf("unexpected sentinel %v for %v", s, tt.in)
					}
				}
			}
			var pgErr *pgconn.PgError
			if tt.wantPG && !errors.As(got, &pgErr) {
				t.Error("driver error must stay reachable with errors.As (the CLI relies on it)")
			}
		})
	}
}

func TestCheckColumn(t *testing.T) {
	for _, ok := range []string{"name", "group_id", "created_at", "a1"} {
		if err := checkColumn(ok); err != nil {
			t.Errorf("checkColumn(%q) = %v, want nil", ok, err)
		}
	}
	for _, bad := range []string{"", "Name", "1abc", "a-b", "a b", "a;drop table x", "a.b", `a"b`, "name)--", "é"} {
		if err := checkColumn(bad); !errors.Is(err, ErrInvalidColumn) {
			t.Errorf("checkColumn(%q) = %v, want ErrInvalidColumn", bad, err)
		}
	}
}

func TestListParamsNormalize(t *testing.T) {
	tests := []struct {
		in            ListParams
		limit, offset int
	}{
		{ListParams{}, 20, 0},
		{ListParams{Page: 3, PerPage: 10}, 10, 20},
		{ListParams{Page: -1, PerPage: 500}, 100, 0},
	}
	for _, tt := range tests {
		l, o := tt.in.normalize()
		if l != tt.limit || o != tt.offset {
			t.Errorf("normalize(%+v) = %d,%d want %d,%d", tt.in, l, o, tt.limit, tt.offset)
		}
	}
}
