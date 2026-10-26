package icloud

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"math/big"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// fakeApple — минимальный idmsa + setup с настоящей серверной стороной SRP.
type fakeApple struct {
	t        *testing.T
	password string
	salt     []byte
	b        *big.Int
	A, B     *big.Int
	trusted  bool
	calls    []string
}

func (f *fakeApple) handler() http.Handler {
	mux := http.NewServeMux()
	width := len(srpN.Bytes())
	mux.HandleFunc("POST /auth/signin/init", func(w http.ResponseWriter, r *http.Request) {
		f.calls = append(f.calls, "init")
		var req struct{ A string }
		_ = json.NewDecoder(r.Body).Decode(&req)
		raw, _ := base64.StdEncoding.DecodeString(req.A)
		f.A = new(big.Int).SetBytes(raw)
		p, _ := srpPassword(f.password, "s2k", f.salt, 100)
		x := hInt(0, f.salt, hInt(0, []byte(":"), p).Bytes())
		v := new(big.Int).Exp(srpG, x, srpN)
		k := hInt(width, srpN.Bytes(), srpG.Bytes())
		f.B = new(big.Int).Mod(new(big.Int).Add(new(big.Int).Mul(k, v), new(big.Int).Exp(srpG, f.b, srpN)), srpN)
		w.Header().Set("X-Apple-ID-Session-Id", "sess-1")
		w.Header().Set("scnt", "scnt-1")
		_ = json.NewEncoder(w).Encode(map[string]any{
			"iteration": 100, "salt": base64.StdEncoding.EncodeToString(f.salt), "protocol": "s2k",
			"b": base64.StdEncoding.EncodeToString(f.B.Bytes()), "c": "ctx",
		})
	})
	mux.HandleFunc("POST /auth/signin/complete", func(w http.ResponseWriter, r *http.Request) {
		f.calls = append(f.calls, "complete")
		if r.Header.Get("X-Apple-ID-Session-Id") != "sess-1" || r.Header.Get("scnt") != "scnt-1" {
			f.t.Errorf("session headers not forwarded")
		}
		var req struct{ M1 string }
		_ = json.NewDecoder(r.Body).Decode(&req)
		p, _ := srpPassword(f.password, "s2k", f.salt, 100)
		x := hInt(0, f.salt, hInt(0, []byte(":"), p).Bytes())
		v := new(big.Int).Exp(srpG, x, srpN)
		u := hInt(width, f.A.Bytes(), f.B.Bytes())
		S := new(big.Int).Exp(new(big.Int).Mul(f.A, new(big.Int).Exp(v, u, srpN)), f.b, srpN)
		K := sha(S.Bytes())
		hN, hg := sha(srpN.Bytes()), sha(pad(srpG.Bytes(), width))
		xor := make([]byte, 32)
		for i := range xor {
			xor[i] = hN[i] ^ hg[i]
		}
		want := sha(xor, sha([]byte("me@example.com")), f.salt, f.A.Bytes(), f.B.Bytes(), K)
		if req.M1 != base64.StdEncoding.EncodeToString(want) {
			w.WriteHeader(401)
			return
		}
		w.Header().Set("X-Apple-Session-Token", "token-1")
		if !f.trusted {
			w.WriteHeader(409)
		}
	})
	mux.HandleFunc("GET /auth", func(w http.ResponseWriter, r *http.Request) {
		f.calls = append(f.calls, "push")
		_, _ = w.Write([]byte(`{"trustedPhoneNumbers":[{"id":1,"numberWithDialCode":"+7 •••-••-12"}]}`))
	})
	mux.HandleFunc("POST /auth/verify/trusteddevice/securitycode", func(w http.ResponseWriter, r *http.Request) {
		f.calls = append(f.calls, "verify")
		var req struct {
			SecurityCode struct{ Code string } `json:"securityCode"`
		}
		_ = json.NewDecoder(r.Body).Decode(&req)
		if req.SecurityCode.Code != "123456" {
			w.WriteHeader(400)
			return
		}
		w.WriteHeader(204)
	})
	mux.HandleFunc("GET /auth/2sv/trust", func(w http.ResponseWriter, r *http.Request) {
		f.calls = append(f.calls, "trust")
		f.trusted = true
		w.Header().Set("X-Apple-TwoSV-Trust-Token", "trust-1")
		w.Header().Set("X-Apple-Session-Token", "token-2")
		w.WriteHeader(204)
	})
	account := func(w http.ResponseWriter) {
		http.SetCookie(w, &http.Cookie{Name: "X-APPLE-WEBAUTH-TOKEN", Value: "web", Path: "/"})
		_ = json.NewEncoder(w).Encode(map[string]any{
			"dsInfo":               map[string]any{"dsid": "42", "hsaVersion": 2},
			"hsaChallengeRequired": !f.trusted, "hsaTrustedBrowser": f.trusted,
			"webservices": map[string]any{"ckdatabasews": map[string]any{"url": "https://ck.example"}},
		})
	}
	mux.HandleFunc("POST /setup/accountLogin", func(w http.ResponseWriter, r *http.Request) {
		f.calls = append(f.calls, "accountLogin")
		account(w)
	})
	mux.HandleFunc("POST /setup/validate", func(w http.ResponseWriter, r *http.Request) {
		f.calls = append(f.calls, "validate")
		if c, err := r.Cookie("X-APPLE-WEBAUTH-TOKEN"); err != nil || c.Value != "web" || !f.trusted {
			w.WriteHeader(421)
			return
		}
		account(w)
	})
	return mux
}

func newFake(t *testing.T) (*fakeApple, *httptest.Server) {
	f := &fakeApple{t: t, password: "s3cret", salt: []byte("0123456789abcdef"), b: big.NewInt(987654321)}
	srv := httptest.NewServer(f.handler())
	t.Cleanup(srv.Close)
	return f, srv
}

func newTestSession(srv *httptest.Server, path string) *Session {
	s := NewSession(path)
	s.AuthURL, s.SetupURL = srv.URL+"/auth", srv.URL+"/setup"
	return s
}

func TestLoginWith2FAThenRestore(t *testing.T) {
	f, srv := newFake(t)
	path := filepath.Join(t.TempDir(), "session.json")
	ctx := context.Background()

	s := newTestSession(srv, path)
	if err := s.Login(ctx, "me@example.com", "s3cret"); !errors.Is(err, ErrNeed2FA) {
		t.Fatalf("Login err = %v, want ErrNeed2FA", err)
	}
	if s.Account() != nil {
		t.Fatal("account must be unavailable before 2FA")
	}
	if ph := s.Phones(); len(ph) != 1 || ph[0].ID != 1 {
		t.Fatalf("phones = %+v", ph)
	}
	var ae *AuthError
	if err := s.Verify2FA(ctx, "000000", 0); !errors.As(err, &ae) {
		t.Fatalf("wrong code err = %v", err)
	}
	if err := s.Verify2FA(ctx, " 123456 ", 0); err != nil {
		t.Fatal(err)
	}
	acc := s.Account()
	if acc == nil || acc.DSID != "42" || acc.PhotosURL != "https://ck.example" {
		t.Fatalf("account = %+v", acc)
	}

	data, _ := os.ReadFile(path)
	if strings.Contains(string(data), "s3cret") {
		t.Fatal("password leaked into session file")
	}
	if fi, _ := os.Stat(path); fi.Mode().Perm() != 0o600 {
		t.Fatalf("session file mode = %v", fi.Mode().Perm())
	}
	if !strings.Contains(string(data), "trust-1") {
		t.Fatal("trust token not persisted")
	}

	s2 := newTestSession(srv, path)
	if err := s2.Restore(ctx); err != nil {
		t.Fatalf("Restore: %v (calls %v)", err, f.calls)
	}
	if s2.Account() == nil || s2.AppleID() != "me@example.com" {
		t.Fatal("restored session has no account")
	}
}

func TestLoginWrongPassword(t *testing.T) {
	_, srv := newFake(t)
	s := newTestSession(srv, filepath.Join(t.TempDir(), "s.json"))
	var ae *AuthError
	err := s.Login(context.Background(), "me@example.com", "wrong")
	if !errors.As(err, &ae) || !strings.Contains(ae.Msg, "пароли приложений") {
		t.Fatalf("err = %v", err)
	}
}

func TestRestoreWithoutSessionNeedsLogin(t *testing.T) {
	_, srv := newFake(t)
	s := newTestSession(srv, filepath.Join(t.TempDir(), "none.json"))
	if err := s.Restore(context.Background()); !errors.Is(err, ErrNeedLogin) {
		t.Fatalf("err = %v", err)
	}
}

func TestParseAuthOptionsFormats(t *testing.T) {
	flat := []byte(`{"trustedPhoneNumbers":[{"id":1,"numberWithDialCode":"+7 ••• 12"}],"authenticationType":"hsa2"}`)
	nested := []byte(`{"phoneNumberVerification":{"trustedPhoneNumbers":[{"id":2,"numberWithDialCode":"+1 ••• 34"}]},"noTrustedDevices":false}`)
	if ph, sum := parseAuthOptions(flat); len(ph) != 1 || ph[0].ID != 1 || strings.Contains(sum, "12") {
		t.Fatalf("flat: %v %q", ph, sum)
	}
	if ph, sum := parseAuthOptions(nested); len(ph) != 1 || ph[0].ID != 2 || strings.Contains(sum, "34") {
		t.Fatalf("nested: %v %q", ph, sum)
	}
	if ph, _ := parseAuthOptions([]byte("<html>")); ph != nil {
		t.Fatal("html must give no phones")
	}
}
