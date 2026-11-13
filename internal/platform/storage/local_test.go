package storage

import (
	"bytes"
	"context"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func newTestStore(t *testing.T) (*Local, string) {
	t.Helper()
	dir := filepath.Join(t.TempDir(), "files")
	l, err := NewLocal(dir)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = l.Close() })
	return l, dir
}

func TestLocal_PutOpenDelete(t *testing.T) {
	l, _ := newTestStore(t)
	ctx := context.Background()

	n, err := l.Put(ctx, "tenant1/file1", strings.NewReader("hello"), 100)
	if err != nil || n != 5 {
		t.Fatalf("put: %d %v", n, err)
	}
	rc, err := l.Open(ctx, "tenant1/file1")
	if err != nil {
		t.Fatal(err)
	}
	got, _ := io.ReadAll(rc)
	_ = rc.Close()
	if string(got) != "hello" {
		t.Fatalf("content %q", got)
	}

	if err := l.Delete(ctx, "tenant1/file1"); err != nil {
		t.Fatal(err)
	}
	if _, err := l.Open(ctx, "tenant1/file1"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("after delete: %v", err)
	}
	if err := l.Delete(ctx, "tenant1/file1"); err != nil {
		t.Fatalf("deleting twice must not fail: %v", err)
	}
}

func TestLocal_TooLargeStoresNothing(t *testing.T) {
	l, dir := newTestStore(t)
	_, err := l.Put(context.Background(), "t/big", bytes.NewReader(make([]byte, 11)), 10)
	if !errors.Is(err, ErrTooLarge) {
		t.Fatalf("got %v", err)
	}
	entries, _ := os.ReadDir(filepath.Join(dir, "t"))
	if len(entries) != 0 {
		t.Fatalf("leftovers: %v", entries)
	}
	// exactly the limit is fine
	if _, err := l.Put(context.Background(), "t/ok", bytes.NewReader(make([]byte, 10)), 10); err != nil {
		t.Fatal(err)
	}
}

func TestLocal_KeysCannotEscape(t *testing.T) {
	l, _ := newTestStore(t)
	ctx := context.Background()
	for _, key := range []string{
		"", "/etc/passwd", "../x", "a/../../x", "a/./b", "a//b", ".hidden", "a/.tmp-x", "a b", "a\\b", "ü", strings.Repeat("a", 301),
	} {
		if _, err := l.Put(ctx, key, strings.NewReader("x"), 10); !errors.Is(err, ErrInvalidKey) {
			t.Errorf("Put(%q): %v", key, err)
		}
		if _, err := l.Open(ctx, key); !errors.Is(err, ErrInvalidKey) {
			t.Errorf("Open(%q): %v", key, err)
		}
		if err := l.Delete(ctx, key); !errors.Is(err, ErrInvalidKey) {
			t.Errorf("Delete(%q): %v", key, err)
		}
	}
}

func TestLocal_SymlinkOutsideIsRefused(t *testing.T) {
	l, dir := newTestStore(t)
	outside := filepath.Join(t.TempDir(), "secret")
	if err := os.WriteFile(outside, []byte("secret"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outside, filepath.Join(dir, "link")); err != nil {
		t.Skip("symlinks not available")
	}
	if rc, err := l.Open(context.Background(), "link"); err == nil {
		_ = rc.Close()
		t.Fatal("a symlink to a file outside the directory must not be readable")
	}
}

func TestLocal_CancelledContext(t *testing.T) {
	l, _ := newTestStore(t)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := l.Put(ctx, "t/x", strings.NewReader("x"), 10); !errors.Is(err, context.Canceled) {
		t.Fatalf("got %v", err)
	}
}

func TestNewLocal_EmptyDir(t *testing.T) {
	if _, err := NewLocal(""); err == nil {
		t.Fatal("empty directory must be refused")
	}
}
