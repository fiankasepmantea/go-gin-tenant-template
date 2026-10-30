package idgen

import (
	"sort"
	"testing"
)

func TestNew_SequentialAndUnique(t *testing.T) {
	const n = 20000

	ids := make([]string, n)
	seen := make(map[string]struct{}, n)
	for i := range ids {
		ids[i] = New()
		if _, dup := seen[ids[i]]; dup {
			t.Fatalf("duplicate ID: %s", ids[i])
		}
		seen[ids[i]] = struct{}{}
	}

	if !sort.StringsAreSorted(ids) {
		t.Fatal("IDs must be in ascending order of creation (UUID v7)")
	}
}

func TestNewUUID_Version7(t *testing.T) {
	if v := NewUUID().Version(); v != 7 {
		t.Fatalf("UUID version = %d, want 7", v)
	}
}

func TestNewOpaqueToken(t *testing.T) {
	a, b := NewOpaqueToken(), NewOpaqueToken()
	if a == b {
		t.Fatal("tokens must be random and different")
	}
	if len(a) != 43 { // 32 bytes base64url without padding
		t.Fatalf("token length = %d, want 43", len(a))
	}
}
