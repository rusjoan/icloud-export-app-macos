package icloud

import (
	"net/http"
	"net/url"
	"testing"
	"time"
)

func TestJarDomainMatchingAndExport(t *testing.T) {
	j := NewJar(nil)
	setup, _ := url.Parse("https://setup.icloud.com/setup/ws/1/accountLogin")
	j.SetCookies(setup, []*http.Cookie{
		{Name: "X-APPLE-WEBAUTH-TOKEN", Value: "tok", Domain: ".icloud.com", Path: "/", Secure: true},
		{Name: "hostonly", Value: "h"},
		{Name: "foreign", Value: "f", Domain: "apple.com"},
	})

	ck, _ := url.Parse("https://p123-ckdatabasews.icloud.com/database/1")
	got := j.Cookies(ck)
	if len(got) != 1 || got[0].Name != "X-APPLE-WEBAUTH-TOKEN" {
		t.Fatalf("ckdatabase cookies = %v", got)
	}
	if n := len(j.Cookies(setup)); n != 2 {
		t.Fatalf("setup cookies = %d, want 2", n)
	}

	restored := NewJar(j.Export())
	if n := len(restored.Cookies(ck)); n != 1 {
		t.Fatalf("restored jar lost cookies: %d", n)
	}
}

func TestJarReplaceAndExpire(t *testing.T) {
	j := NewJar(nil)
	u, _ := url.Parse("https://www.icloud.com/")
	j.SetCookies(u, []*http.Cookie{{Name: "a", Value: "1"}})
	j.SetCookies(u, []*http.Cookie{{Name: "a", Value: "2"}})
	if got := j.Cookies(u); len(got) != 1 || got[0].Value != "2" {
		t.Fatalf("replace failed: %v", got)
	}
	j.SetCookies(u, []*http.Cookie{{Name: "a", MaxAge: -1}})
	if got := j.Cookies(u); len(got) != 0 {
		t.Fatalf("delete failed: %v", got)
	}
	j.SetCookies(u, []*http.Cookie{{Name: "b", Value: "x", Expires: time.Now().Add(time.Hour)}})
	j.now = func() time.Time { return time.Now().Add(2 * time.Hour) }
	if got := j.Export(); len(got) != 0 {
		t.Fatalf("expired cookie exported: %v", got)
	}
}
