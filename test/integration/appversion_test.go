//go:build integration

package integration

import (
	"archive/zip"
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"math/rand"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
)

const (
	vBase   = "/api/v1/app-versions"
	vPublic = "/api/v1/public/app-versions"
)

// makeZip builds a zip archive with the given entries (name -> content).
func makeZip(t *testing.T, entries map[string]string) []byte {
	t.Helper()
	var buf bytes.Buffer
	w := zip.NewWriter(&buf)
	for name, body := range entries {
		f, err := w.Create(name)
		if err != nil {
			t.Fatal(err)
		}
		_, _ = f.Write([]byte(body))
	}
	if err := w.Close(); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}

func apkBytes(t *testing.T, filler int) []byte {
	// the filler must not compress, or the archive would be far smaller than asked
	rnd := rand.New(rand.NewSource(int64(filler)))
	noise := make([]byte, filler)
	_, _ = rnd.Read(noise)
	return makeZip(t, map[string]string{"AndroidManifest.xml": "<manifest/>", "classes.dex": string(noise)})
}

type versionOut struct {
	ID            string  `json:"app_version_id"`
	Platform      string  `json:"platform"`
	Version       string  `json:"version"`
	MinSupported  string  `json:"min_supported_version"`
	IsForceUpdate bool    `json:"is_force_update"`
	PackageSize   *int64  `json:"package_size"`
	PackageSHA256 *string `json:"package_sha256"`
}

func versionOf(t *testing.T, res apiResp) versionOut {
	t.Helper()
	var v versionOut
	if err := json.Unmarshal(res.Body.Data, &v); err != nil || v.ID == "" {
		t.Fatalf("no version in response: %d %s", res.Status, res.Raw)
	}
	return v
}

func (e *rbacEnv) putPackage(t *testing.T, tok, id, filename string, content []byte) apiResp {
	t.Helper()
	body, ct := form(t, filename, content)
	return call(t, e.r, http.MethodPut, vBase+"/"+id+"/package", tok, body, "Content-Type", ct)
}

func (e *rbacEnv) get(t *testing.T, target string, headers ...string) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequestWithContext(context.Background(), http.MethodGet, target, nil)
	for i := 0; i+1 < len(headers); i += 2 {
		req.Header.Set(headers[i], headers[i+1])
	}
	w := httptest.NewRecorder()
	e.r.ServeHTTP(w, req)
	return w
}

func platformAdmin(t *testing.T) (*rbacEnv, string) {
	t.Helper()
	e := setupRBAC(t)
	e.sync(t)
	settle()
	return e, login(t, e.r, "/api/v1/auth/platform/login", "", "root", "password-of-the-root")
}

func TestAppVersion_AdminManagesVersions(t *testing.T) {
	e, root := platformAdmin(t)

	create := func(body string) apiResp { return call(t, e.r, http.MethodPost, vBase, root, body) }
	v19 := versionOf(t, create(`{"platform":"android","version":"1.9","min_supported_version":"1.5","release_notes":"nine","store_url":"https://play.example.test/app"}`))
	if v19.Version != "1.9.0" || v19.MinSupported != "1.5.0" {
		t.Fatalf("versions are stored canonical: %+v", v19)
	}
	v110 := versionOf(t, create(`{"platform":"android","version":"1.10.0"}`))
	if v110.MinSupported != "0.0.0" {
		t.Fatalf("no minimum means 0.0.0: %+v", v110)
	}
	create(`{"platform":"ios","version":"2.0.0"}`)

	// the same number under another spelling is a duplicate
	if res := create(`{"platform":"android","version":"1.10"}`); res.Status != 409 {
		t.Fatalf("duplicate: %d %s", res.Status, res.Raw)
	}
	for name, body := range map[string]string{
		"letters":        `{"platform":"android","version":"1.2.x"}`,
		"v prefix":       `{"platform":"android","version":"v1.2.0"}`,
		"unknown os":     `{"platform":"palm","version":"1.0.0"}`,
		"min above":      `{"platform":"android","version":"1.0.0","min_supported_version":"2.0.0"}`,
		"bad min":        `{"platform":"android","version":"1.1.0","min_supported_version":"x"}`,
		"script url":     `{"platform":"android","version":"1.2.0","store_url":"javascript:alert(1)"}`,
		"missing fields": `{}`,
	} {
		if res := create(body); res.Status != 422 {
			t.Errorf("%s: %d %s", name, res.Status, res.Raw)
		}
	}

	// list, filter, get
	if res := call(t, e.r, http.MethodGet, vBase+"?platform=android", root, ""); res.Status != 200 || strings.Contains(res.Raw, `"ios"`) || !strings.Contains(res.Raw, `"total_data":2`) {
		t.Fatalf("list: %d %s", res.Status, res.Raw)
	}
	if res := call(t, e.r, http.MethodGet, vBase+"/"+v19.ID, root, ""); res.Status != 200 || strings.Contains(res.Raw, "package_key") {
		t.Fatalf("get: %d %s", res.Status, res.Raw)
	}

	// patch: version and platform cannot change, the rest can
	if res := call(t, e.r, http.MethodPatch, vBase+"/"+v19.ID, root, `{"is_force_update":true,"release_notes":"nine!","min_supported_version":"1.8"}`); res.Status != 200 ||
		!strings.Contains(res.Raw, `"is_force_update":true`) || !strings.Contains(res.Raw, `"1.8.0"`) {
		t.Fatalf("patch: %d %s", res.Status, res.Raw)
	}
	if res := call(t, e.r, http.MethodPatch, vBase+"/"+v19.ID, root, `{"min_supported_version":"3.0.0"}`); res.Status != 422 {
		t.Fatalf("min above version: %d", res.Status)
	}
	if res := call(t, e.r, http.MethodPatch, vBase+"/"+v19.ID, root, `{"store_url":"ftp://x.test/a"}`); res.Status != 422 {
		t.Fatalf("ftp url: %d", res.Status)
	}
	if res := call(t, e.r, http.MethodPatch, vBase+"/"+v19.ID, root, `{}`); res.Status != 400 {
		t.Fatalf("empty patch: %d", res.Status)
	}
	if res := call(t, e.r, http.MethodGet, vBase+"/00000000-0000-7000-8000-000000000001", root, ""); res.Status != 404 {
		t.Fatalf("unknown id: %d", res.Status)
	}
}

func TestAppVersion_OnlyPlatformAdminsManage(t *testing.T) {
	e, _ := platformAdmin(t)
	ctx := context.Background()
	if _, err := e.prov.CreateGroup(ctx, "tokoa", "admin", ""); err != nil {
		t.Fatal(err)
	}
	if err := e.prov.SetUserGroup(ctx, "tokoa", "alice", "admin"); err != nil {
		t.Fatal(err)
	}
	// "--all" grants every endpoint that CAN be granted: platform endpoints are not among them
	if _, err := e.prov.GrantEndpoints(ctx, "tokoa", "admin", true, nil); err != nil {
		t.Fatal(err)
	}
	settle()
	alice := login(t, e.r, "/api/v1/auth/login", "tokoa", "alice", "password-of-alice-a")

	for _, r := range []struct{ method, path string }{
		{http.MethodGet, vBase}, {http.MethodPost, vBase}, {http.MethodDelete, vBase + "/00000000-0000-7000-8000-000000000001"},
	} {
		if res := call(t, e.r, r.method, r.path, alice, `{}`); res.Status != 403 {
			t.Errorf("tenant admin %s %s: %d", r.method, r.path, res.Status)
		}
		if res := call(t, e.r, r.method, r.path, "", `{}`); res.Status != 401 {
			t.Errorf("anonymous %s %s: %d", r.method, r.path, res.Status)
		}
	}

	// the public routes are not endpoints: nothing to grant, nothing in the table
	if n := e.count(t, "SELECT count(*) FROM endpoints WHERE path LIKE '/api/v1/public/%'"); n != 0 {
		t.Errorf("public routes were synchronized: %d", n)
	}
	if n := e.count(t, "SELECT count(*) FROM endpoints WHERE path LIKE '/api/v1/app-versions%' AND scope = 'platform'"); n != 6 {
		t.Errorf("platform endpoints = %d, want 6", n)
	}
}

func TestAppVersion_PackageUpload(t *testing.T) {
	e, root := platformAdmin(t)
	and := versionOf(t, call(t, e.r, http.MethodPost, vBase, root, `{"platform":"android","version":"1.0.0"}`))
	ios := versionOf(t, call(t, e.r, http.MethodPost, vBase, root, `{"platform":"ios","version":"1.0.0"}`))

	apk := apkBytes(t, 2000)
	if res := e.putPackage(t, root, ios.ID, "a.apk", apk); res.Status != 422 {
		t.Errorf("package for ios: %d", res.Status)
	}
	for name, content := range map[string][]byte{
		"text":            []byte("hello"),
		"zip no manifest": makeZip(t, map[string]string{"readme.txt": "x"}),
		"png":             pngBytes(100),
		"empty":           nil,
	} {
		if res := e.putPackage(t, root, and.ID, "a.apk", content); res.Status != 422 {
			t.Errorf("%s: %d %s", name, res.Status, res.Raw)
		}
	}
	if res := e.putPackage(t, root, and.ID, "a.apk", apkBytes(t, 300<<10)); res.Status != 413 {
		t.Errorf("too large: %d", res.Status)
	}
	if res := call(t, e.r, http.MethodPut, vBase+"/"+and.ID+"/package", root, `{"a":1}`); res.Status != 400 {
		t.Errorf("json instead of multipart: %d", res.Status)
	}
	if res := e.putPackage(t, root, "00000000-0000-7000-8000-000000000001", "a.apk", apk); res.Status != 404 {
		t.Errorf("unknown version: %d", res.Status)
	}
	if left, _ := filepath.Glob(filepath.Join(e.blobDir, "app-versions", "*")); len(left) != 0 {
		t.Fatalf("refused packages left content: %v", left)
	}

	res := e.putPackage(t, root, and.ID, "app.apk", apk)
	if res.Status != 200 {
		t.Fatalf("upload: %d %s", res.Status, res.Raw)
	}
	v := versionOf(t, res)
	sum := sha256.Sum256(apk)
	if v.PackageSize == nil || *v.PackageSize != int64(len(apk)) || v.PackageSHA256 == nil || *v.PackageSHA256 != hex.EncodeToString(sum[:]) {
		t.Fatalf("package metadata: %+v", v)
	}

	// a second upload replaces the first: one file on disk, the new content served
	apk2 := apkBytes(t, 3000)
	if res := e.putPackage(t, root, and.ID, "app.apk", apk2); res.Status != 200 {
		t.Fatalf("replace: %d %s", res.Status, res.Raw)
	}
	if left, _ := filepath.Glob(filepath.Join(e.blobDir, "app-versions", "*")); len(left) != 1 {
		t.Fatalf("replacing left %d files", len(left))
	}
	if w := e.get(t, vPublic+"/"+and.ID+"/download"); !bytes.Equal(w.Body.Bytes(), apk2) {
		t.Fatal("the new package is not the one served")
	}

	// deleting the version removes the package
	if res := call(t, e.r, http.MethodDelete, vBase+"/"+and.ID, root, ""); res.Status != 200 {
		t.Fatalf("delete: %d", res.Status)
	}
	if left, _ := filepath.Glob(filepath.Join(e.blobDir, "app-versions", "*")); len(left) != 0 {
		t.Fatalf("delete left content: %v", left)
	}
	if w := e.get(t, vPublic+"/"+and.ID+"/download"); w.Code != 404 {
		t.Fatalf("download after delete: %d", w.Code)
	}
}

func TestAppVersion_PublicLatestCheckAndDownload(t *testing.T) {
	e, root := platformAdmin(t)
	mk := func(body string) versionOut { return versionOf(t, call(t, e.r, http.MethodPost, vBase, root, body)) }
	mk(`{"platform":"android","version":"1.9.0","min_supported_version":"1.5.0"}`)
	latest := mk(`{"platform":"android","version":"1.10.0","min_supported_version":"1.5.0","release_notes":"ten"}`)
	mk(`{"platform":"ios","version":"2.0.0","store_url":"https://apps.example.test/x"}`)

	// no token anywhere below
	w := e.get(t, vPublic+"/latest?platform=android")
	if w.Code != 200 || !strings.Contains(w.Body.String(), `"version":"1.10.0"`) || !strings.Contains(w.Body.String(), `"package":null`) ||
		strings.Contains(w.Body.String(), "storage") || strings.Contains(w.Body.String(), "app_version_id") {
		t.Fatalf("latest (1.10 is higher than 1.9): %d %s", w.Code, w.Body)
	}
	if w := e.get(t, vPublic+"/latest?platform=ios"); w.Code != 200 || !strings.Contains(w.Body.String(), "2.0.0") {
		t.Fatalf("ios: %d %s", w.Code, w.Body)
	}
	if w := e.get(t, vPublic+"/latest?platform=web"); w.Code != 404 {
		t.Fatalf("no web versions: %d", w.Code)
	}
	if w := e.get(t, vPublic+"/latest?platform=palm"); w.Code != 422 {
		t.Fatalf("unknown platform: %d", w.Code)
	}
	if w := e.get(t, vPublic+"/latest/download?platform=android"); w.Code != 404 {
		t.Fatalf("download without a package: %d", w.Code)
	}

	apk := apkBytes(t, 5000)
	if res := e.putPackage(t, root, latest.ID, "x.apk", apk); res.Status != 200 {
		t.Fatalf("upload: %d %s", res.Status, res.Raw)
	}
	w = e.get(t, vPublic+"/latest?platform=android")
	if !strings.Contains(w.Body.String(), `"download_url":"/api/v1/public/app-versions/`+latest.ID+`/download"`) || !strings.Contains(w.Body.String(), `"size":`+itoa(len(apk))) {
		t.Fatalf("latest with package: %s", w.Body)
	}

	// check: behind, far behind (below the minimum), current, ahead
	type check struct {
		Data struct {
			Update   bool   `json:"update_available"`
			Force    bool   `json:"force_update"`
			Current  string `json:"current_version"`
			Latest   string `json:"latest_version"`
			Download string `json:"download_url"`
		} `json:"data"`
	}
	for _, c := range []struct {
		version       string
		update, force bool
	}{{"1.9.0", true, false}, {"1.4.9", true, true}, {"1.10", false, false}, {"2.0.0", false, false}, {"1.9", true, false}} {
		var out check
		w := e.get(t, vPublic+"/check?platform=android&version="+c.version)
		if err := json.Unmarshal(w.Body.Bytes(), &out); err != nil || w.Code != 200 || out.Data.Update != c.update || out.Data.Force != c.force || out.Data.Latest != "1.10.0" {
			t.Errorf("check %s: %d %s", c.version, w.Code, w.Body)
		}
		if c.update && out.Data.Download == "" {
			t.Errorf("check %s: no download_url", c.version)
		}
	}
	if w := e.get(t, vPublic+"/check?platform=android&version=abc"); w.Code != 422 {
		t.Errorf("bad version: %d", w.Code)
	}
	if w := e.get(t, vPublic+"/check?platform=android"); w.Code != 422 {
		t.Errorf("missing version: %d", w.Code)
	}

	// marking the latest release as a forced update makes every older device update
	if res := call(t, e.r, http.MethodPatch, vBase+"/"+latest.ID, root, `{"is_force_update":true}`); res.Status != 200 {
		t.Fatal(res.Raw)
	}
	var forced check
	_ = json.Unmarshal(e.get(t, vPublic+"/check?platform=android&version=1.9.0").Body.Bytes(), &forced)
	if !forced.Data.Force {
		t.Error("is_force_update must force devices that are behind")
	}
	_ = json.Unmarshal(e.get(t, vPublic+"/check?platform=android&version=1.10.0").Body.Bytes(), &forced)
	if forced.Data.Force {
		t.Error("a device on the latest version is never forced")
	}

	// download: the content, the headers, resume, and conditional requests
	w = e.get(t, vPublic+"/latest/download?platform=android")
	sum := sha256.Sum256(apk)
	etag := `"` + hex.EncodeToString(sum[:]) + `"`
	if w.Code != 200 || !bytes.Equal(w.Body.Bytes(), apk) || w.Header().Get("Content-Type") != "application/vnd.android.package-archive" ||
		w.Header().Get("ETag") != etag || !strings.Contains(w.Header().Get("Content-Disposition"), "android-1.10.0.apk") ||
		w.Header().Get("X-Content-Type-Options") != "nosniff" {
		t.Fatalf("download: %d %v", w.Code, w.Header())
	}
	if w := e.get(t, vPublic+"/"+latest.ID+"/download", "Range", "bytes=10-19"); w.Code != 206 || !bytes.Equal(w.Body.Bytes(), apk[10:20]) {
		t.Fatalf("range: %d %d bytes", w.Code, w.Body.Len())
	}
	if w := e.get(t, vPublic+"/"+latest.ID+"/download", "If-None-Match", etag); w.Code != 304 || w.Body.Len() != 0 {
		t.Fatalf("conditional: %d", w.Code)
	}
	if w := e.get(t, vPublic+"/not-a-uuid/download"); w.Code != 404 {
		t.Fatalf("bad id: %d", w.Code)
	}

	// the highest version is deleted: the next one is the latest
	if res := call(t, e.r, http.MethodDelete, vBase+"/"+latest.ID, root, ""); res.Status != 200 {
		t.Fatal(res.Raw)
	}
	if w := e.get(t, vPublic+"/latest?platform=android"); !strings.Contains(w.Body.String(), `"version":"1.9.0"`) {
		t.Fatalf("after delete: %s", w.Body)
	}
}

func itoa(n int) string {
	b, _ := json.Marshal(n)
	return string(b)
}
