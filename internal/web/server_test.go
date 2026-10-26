package web

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"

	"github.com/rusjoan/icloud-export-app-macos/internal/icloud"
)

func newTestServer(t *testing.T) (*Server, http.Handler) {
	sess := icloud.NewSession(filepath.Join(t.TempDir(), "s.json"))
	s := New(context.Background(), sess)
	return s, s.handler("127.0.0.1:5555")
}

func do(h http.Handler, method, target, host, token, body string) *httptest.ResponseRecorder {
	req := httptest.NewRequest(method, target, strings.NewReader(body))
	req.Host = host
	if token != "" {
		req.Header.Set("X-Token", token)
	}
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	return rec
}

func TestAPIRequiresTokenAndOwnHost(t *testing.T) {
	s, h := newTestServer(t)
	if rec := do(h, "GET", "/api/state", "127.0.0.1:5555", "", ""); rec.Code != 403 {
		t.Fatalf("no token: %d", rec.Code)
	}
	if rec := do(h, "GET", "/api/state", "evil.example:5555", s.token, ""); rec.Code != 403 {
		t.Fatalf("foreign host: %d", rec.Code)
	}
	rec := do(h, "GET", "/api/state", "localhost:5555", s.token, "")
	if rec.Code != 200 {
		t.Fatalf("state: %d %s", rec.Code, rec.Body)
	}
	var st stateDTO
	_ = json.Unmarshal(rec.Body.Bytes(), &st)
	if st.Auth == "" {
		t.Fatal("auth state missing")
	}
	if rec := do(h, "GET", "/api/state?t="+s.token, "127.0.0.1:5555", "", ""); rec.Code != 200 {
		t.Fatalf("query token: %d", rec.Code)
	}
}

func TestExportValidation(t *testing.T) {
	s, h := newTestServer(t)
	rec := do(h, "POST", "/api/export", "127.0.0.1:5555", s.token, `{"albums":["x"],"dest":"relative/dir","layout":"date"}`)
	if rec.Code != 400 || !strings.Contains(rec.Body.String(), "полный путь") {
		t.Fatalf("relative dest: %d %s", rec.Code, rec.Body)
	}
	rec = do(h, "POST", "/api/export", "127.0.0.1:5555", s.token, `{"albums":["nope"],"dest":"/tmp/x"}`)
	if rec.Code != 400 || !strings.Contains(rec.Body.String(), "альбом") {
		t.Fatalf("unknown album: %d %s", rec.Code, rec.Body)
	}
	rec = do(h, "POST", "/api/pick-folder", "127.0.0.1:5555", s.token, "")
	if rec.Code != 501 {
		t.Fatalf("picker without window: %d", rec.Code)
	}
}

func TestLoginRequiresFields(t *testing.T) {
	s, h := newTestServer(t)
	rec := do(h, "POST", "/api/login", "127.0.0.1:5555", s.token, `{"appleId":"","password":""}`)
	if rec.Code != 400 {
		t.Fatalf("empty login: %d", rec.Code)
	}
}

func TestDestInfoExpandsHome(t *testing.T) {
	s, h := newTestServer(t)
	rec := do(h, "GET", "/api/dest?path=~/Pictures/x", "127.0.0.1:5555", s.token, "")
	if rec.Code != 200 || strings.Contains(rec.Body.String(), "~") {
		t.Fatalf("dest: %d %s", rec.Code, rec.Body)
	}
}
