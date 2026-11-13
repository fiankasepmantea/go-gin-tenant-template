//go:build integration

package integration

import (
	"bytes"
	"context"
	"encoding/json"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
)

var pngHeader = []byte("\x89PNG\r\n\x1a\n")

func pngBytes(n int) []byte {
	return append(append([]byte{}, pngHeader...), bytes.Repeat([]byte{7}, n)...)
}

// form builds a multipart body with an optional file part and extra text fields.
func form(t *testing.T, filename string, content []byte, fields ...string) (body, contentType string) {
	t.Helper()
	var buf bytes.Buffer
	w := multipart.NewWriter(&buf)
	for i := 0; i+1 < len(fields); i += 2 {
		if err := w.WriteField(fields[i], fields[i+1]); err != nil {
			t.Fatal(err)
		}
	}
	if filename != "" {
		part, err := w.CreateFormFile("file", filename)
		if err != nil {
			t.Fatal(err)
		}
		_, _ = part.Write(content)
	}
	_ = w.Close()
	return buf.String(), w.FormDataContentType()
}

func (e *rbacEnv) upload(t *testing.T, tok, filename string, content []byte, fields ...string) apiResp {
	t.Helper()
	body, ct := form(t, filename, content, fields...)
	return call(t, e.r, http.MethodPost, "/api/v1/files", tok, body, "Content-Type", ct)
}

type fileOut struct {
	ID          string `json:"file_id"`
	Name        string `json:"name"`
	ContentType string `json:"content_type"`
	Size        int64  `json:"size"`
	Purpose     string `json:"purpose"`
}

func fileOf(t *testing.T, res apiResp) fileOut {
	t.Helper()
	var f fileOut
	if err := json.Unmarshal(res.Body.Data, &f); err != nil || f.ID == "" {
		t.Fatalf("no file in response: %d %s", res.Status, res.Raw)
	}
	return f
}

func (e *rbacEnv) raw(t *testing.T, target, tok string) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequestWithContext(context.Background(), http.MethodGet, target, nil)
	if tok != "" {
		req.Header.Set("Authorization", "Bearer "+tok)
	}
	w := httptest.NewRecorder()
	e.r.ServeHTTP(w, req)
	return w
}

func TestFilesAPI_UploadDownloadDelete(t *testing.T) {
	e, tok := userAdmin(t)
	content := pngBytes(2000)

	res := e.upload(t, tok, `..\..\etc/avatar.png`, content, "purpose", "avatar")
	if res.Status != 201 || strings.Contains(res.Raw, "storage_key") {
		t.Fatalf("upload: %d %s", res.Status, res.Raw)
	}
	f := fileOf(t, res)
	if f.Name != "avatar.png" || f.ContentType != "image/png" || f.Size != int64(len(content)) || f.Purpose != "avatar" {
		t.Fatalf("metadata: %+v", f)
	}

	// metadata and list (with a filter)
	if res := call(t, e.r, http.MethodGet, "/api/v1/files/"+f.ID, tok, ""); res.Status != 200 {
		t.Fatalf("get: %d %s", res.Status, res.Raw)
	}
	if res := call(t, e.r, http.MethodGet, "/api/v1/files?purpose=avatar", tok, ""); res.Status != 200 || !strings.Contains(res.Raw, f.ID) {
		t.Fatalf("list: %d %s", res.Status, res.Raw)
	}
	if res := call(t, e.r, http.MethodGet, "/api/v1/files?purpose=other", tok, ""); strings.Contains(res.Raw, f.ID) {
		t.Fatalf("filter: %s", res.Raw)
	}

	// content: the same bytes, with the headers that keep a browser from running it
	w := e.raw(t, "/api/v1/files/"+f.ID+"/content", tok)
	if w.Code != 200 || !bytes.Equal(w.Body.Bytes(), content) {
		t.Fatalf("content: %d, %d bytes", w.Code, w.Body.Len())
	}
	h := w.Header()
	if h.Get("Content-Type") != "image/png" || h.Get("X-Content-Type-Options") != "nosniff" ||
		!strings.Contains(h.Get("Content-Security-Policy"), "sandbox") || !strings.HasPrefix(h.Get("Content-Disposition"), "inline") {
		t.Fatalf("headers: %v", h)
	}
	if w := e.raw(t, "/api/v1/files/"+f.ID+"/content?download=1", tok); !strings.HasPrefix(w.Header().Get("Content-Disposition"), "attachment") {
		t.Fatalf("download disposition: %v", w.Header())
	}
	if w := e.raw(t, "/api/v1/files/"+f.ID+"/content", ""); w.Code != 401 {
		t.Fatalf("anonymous content: %d", w.Code)
	}

	// delete: row and content are gone
	if res := call(t, e.r, http.MethodDelete, "/api/v1/files/"+f.ID, tok, ""); res.Status != 200 {
		t.Fatalf("delete: %d %s", res.Status, res.Raw)
	}
	if w := e.raw(t, "/api/v1/files/"+f.ID+"/content", tok); w.Code != 404 {
		t.Fatalf("content after delete: %d", w.Code)
	}
	left, _ := filepath.Glob(filepath.Join(e.blobDir, "*", "*"))
	if len(left) != 0 {
		t.Fatalf("content left on disk: %v", left)
	}
}

func TestFilesAPI_Validation(t *testing.T) {
	e, tok := userAdmin(t)

	html := []byte("<html><script>alert(1)</script></html>")
	svg := []byte(`<svg xmlns="http://www.w3.org/2000/svg"><script>alert(1)</script></svg>`)
	cases := []struct {
		name     string
		filename string
		content  []byte
		fields   []string
		want     int
	}{
		{"html named png", "evil.png", html, nil, 422},
		{"svg", "x.svg", svg, nil, 422},
		{"executable", "a.exe", []byte("MZ\x90\x00\x03"), nil, 422},
		{"empty", "a.png", nil, nil, 422},
		{"too large", "big.png", pngBytes(70 << 10), nil, 413},
		{"bad purpose", "a.png", pngBytes(10), []string{"purpose", "Avatar!"}, 422},
		{"no file", "", nil, []string{"purpose", "avatar"}, 422},
	}
	for _, c := range cases {
		res := e.upload(t, tok, c.filename, c.content, c.fields...)
		if res.Status != c.want {
			t.Errorf("%s: got %d, want %d: %s", c.name, res.Status, c.want, res.Raw)
		}
	}
	if res := call(t, e.r, http.MethodPost, "/api/v1/files", tok, `{"file":"x"}`); res.Status != 400 {
		t.Errorf("json body: %d", res.Status)
	}
	// nothing was kept from the refused uploads
	left, _ := filepath.Glob(filepath.Join(e.blobDir, "*", "*"))
	if len(left) != 0 {
		t.Fatalf("refused uploads left content: %v", left)
	}
	if res := call(t, e.r, http.MethodGet, "/api/v1/files", tok, ""); !strings.Contains(res.Raw, `"total_data":0`) {
		t.Fatalf("refused uploads left rows: %s", res.Raw)
	}
}

func TestFilesAPI_TenantIsolation(t *testing.T) {
	e, tok := userAdmin(t)
	ctx := context.Background()
	f := fileOf(t, e.upload(t, tok, "a.png", pngBytes(100)))

	// an admin of the other tenant sees nothing of it, by id or in the list
	if _, err := e.prov.CreateGroup(ctx, "tokob", "admin", ""); err != nil {
		t.Fatal(err)
	}
	if err := e.prov.SetUserGroup(ctx, "tokob", "alice", "admin"); err != nil {
		t.Fatal(err)
	}
	if _, err := e.prov.GrantEndpoints(ctx, "tokob", "admin", true, nil); err != nil {
		t.Fatal(err)
	}
	settle()
	other := login(t, e.r, "/api/v1/auth/login", "tokob", "alice", "password-of-alice-b")

	if w := e.raw(t, "/api/v1/files/"+f.ID+"/content", other); w.Code != 404 {
		t.Fatalf("content of another tenant: %d", w.Code)
	}
	if res := call(t, e.r, http.MethodGet, "/api/v1/files/"+f.ID, other, ""); res.Status != 404 {
		t.Fatalf("metadata of another tenant: %d", res.Status)
	}
	if res := call(t, e.r, http.MethodDelete, "/api/v1/files/"+f.ID, other, ""); res.Status != 404 {
		t.Fatalf("delete of another tenant: %d", res.Status)
	}
	if res := call(t, e.r, http.MethodGet, "/api/v1/files", other, ""); strings.Contains(res.Raw, f.ID) {
		t.Fatalf("list leaks: %s", res.Raw)
	}
	// the file is still there for its owner
	if w := e.raw(t, "/api/v1/files/"+f.ID+"/content", tok); w.Code != 200 {
		t.Fatalf("owner lost the file: %d", w.Code)
	}
	// content is kept under the tenant of the file
	if matches, _ := filepath.Glob(filepath.Join(e.blobDir, "*", f.ID)); len(matches) != 1 {
		t.Fatalf("expected one blob, got %v", matches)
	}
}

func TestFilesAPI_NeedsAGrant(t *testing.T) {
	e, _ := userAdmin(t)
	if _, err := e.prov.CreateTenantUser(context.Background(), "tokoa", "dina", "dina@a.test", "Dina", "password-of-dina-aa"); err != nil {
		t.Fatal(err)
	}
	dina := login(t, e.r, "/api/v1/auth/login", "tokoa", "dina", "password-of-dina-aa")
	if res := e.upload(t, dina, "a.png", pngBytes(10)); res.Status != 403 {
		t.Fatalf("user without grants: %d", res.Status)
	}
}
