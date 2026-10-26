package icloud

import (
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"
)

// Jar — простой cookie jar, который можно сохранить на диск (стандартный
// net/http/cookiejar содержимое наружу не отдаёт).
type Jar struct {
	mu      sync.Mutex
	cookies []StoredCookie
	now     func() time.Time
}

type StoredCookie struct {
	Name     string    `json:"name"`
	Value    string    `json:"value"`
	Domain   string    `json:"domain"`
	Path     string    `json:"path"`
	Expires  time.Time `json:"expires,omitzero"`
	Secure   bool      `json:"secure,omitempty"`
	HostOnly bool      `json:"hostOnly,omitempty"`
}

func NewJar(saved []StoredCookie) *Jar {
	return &Jar{cookies: append([]StoredCookie(nil), saved...), now: time.Now}
}

func (j *Jar) SetCookies(u *url.URL, cookies []*http.Cookie) {
	j.mu.Lock()
	defer j.mu.Unlock()
	host := strings.ToLower(u.Hostname())
	now := j.now()
	for _, c := range cookies {
		sc := StoredCookie{Name: c.Name, Value: c.Value, Path: c.Path, Secure: c.Secure}
		if d := strings.TrimPrefix(strings.ToLower(c.Domain), "."); d != "" {
			if host != d && !strings.HasSuffix(host, "."+d) {
				continue // чужой домен
			}
			sc.Domain = d
		} else {
			sc.Domain, sc.HostOnly = host, true
		}
		if sc.Path == "" || !strings.HasPrefix(sc.Path, "/") {
			sc.Path = "/"
		}
		remove := false
		switch {
		case c.MaxAge < 0:
			remove = true
		case c.MaxAge > 0:
			sc.Expires = now.Add(time.Duration(c.MaxAge) * time.Second)
		case !c.Expires.IsZero():
			sc.Expires = c.Expires
			remove = !c.Expires.After(now)
		}
		j.cookies = filterCookies(j.cookies, func(o StoredCookie) bool {
			return !(o.Name == sc.Name && o.Domain == sc.Domain && o.Path == sc.Path)
		})
		if !remove {
			j.cookies = append(j.cookies, sc)
		}
	}
}

func (j *Jar) Cookies(u *url.URL) []*http.Cookie {
	j.mu.Lock()
	defer j.mu.Unlock()
	host := strings.ToLower(u.Hostname())
	path := u.Path
	if path == "" {
		path = "/"
	}
	now := j.now()
	var out []*http.Cookie
	for _, c := range j.cookies {
		if !c.Expires.IsZero() && !c.Expires.After(now) {
			continue
		}
		if c.HostOnly && host != c.Domain || !c.HostOnly && host != c.Domain && !strings.HasSuffix(host, "."+c.Domain) {
			continue
		}
		if !strings.HasPrefix(path, c.Path) || c.Secure && u.Scheme != "https" {
			continue
		}
		out = append(out, &http.Cookie{Name: c.Name, Value: c.Value})
	}
	return out
}

// Export возвращает непросроченные cookies для сохранения.
func (j *Jar) Export() []StoredCookie {
	j.mu.Lock()
	defer j.mu.Unlock()
	now := j.now()
	return filterCookies(append([]StoredCookie(nil), j.cookies...), func(c StoredCookie) bool {
		return c.Expires.IsZero() || c.Expires.After(now)
	})
}

func (j *Jar) Clear() {
	j.mu.Lock()
	j.cookies = nil
	j.mu.Unlock()
}

func filterCookies(in []StoredCookie, keep func(StoredCookie) bool) []StoredCookie {
	out := in[:0]
	for _, c := range in {
		if keep(c) {
			out = append(out, c)
		}
	}
	return out
}
