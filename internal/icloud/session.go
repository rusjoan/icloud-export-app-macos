package icloud

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

// Logf — журнал запросов (по умолчанию выключен). Пароли, токены и тела не пишутся.
var Logf = func(format string, args ...any) {}

const (
	defaultAuthURL  = "https://idmsa.apple.com/appleauth/auth"
	defaultSetupURL = "https://setup.icloud.com/setup/ws/1"
	homeURL         = "https://www.icloud.com"
	widgetKey       = "d39ba9916b7251055b22c7f910e2ea796ee65e98b2ddecea8f5dde8d9d1a815d"
	userAgent       = "Mozilla/5.0 (Macintosh; Intel Mac OS X 10_15_7) AppleWebKit/605.1.15 (KHTML, like Gecko) Version/18.0 Safari/605.1.15"
)

var (
	ErrNeedLogin = errors.New("нужен вход в iCloud")
	ErrNeed2FA   = errors.New("нужен код двухфакторной аутентификации")
)

// AuthError — понятная пользователю ошибка входа.
type AuthError struct{ Msg string }

func (e *AuthError) Error() string { return e.Msg }

// Phone — доверенный номер для получения кода по SMS.
type Phone struct {
	ID     int    `json:"id"`
	Number string `json:"numberWithDialCode"`
}

// Account — то, что нужно сервисам после входа.
type Account struct {
	DSID       string
	PhotosURL  string // webservices.ckdatabasews.url
	FullName   string
	AppleID    string
	needs2FA   bool
	hsaVersion int
}

type persisted struct {
	AppleID        string         `json:"appleId"`
	ClientID       string         `json:"clientId"`
	SessionID      string         `json:"sessionId,omitempty"`
	Scnt           string         `json:"scnt,omitempty"`
	SessionToken   string         `json:"sessionToken,omitempty"`
	TrustToken     string         `json:"trustToken,omitempty"`
	AccountCountry string         `json:"accountCountry,omitempty"`
	Cookies        []StoredCookie `json:"cookies,omitempty"`
}

// Session — сессия iCloud: вход по SRP, 2FA, сохранение между запусками.
type Session struct {
	AuthURL  string
	SetupURL string

	mu      sync.Mutex
	path    string
	jar     *Jar
	http    *http.Client
	st      persisted
	account *Account
	phones  []Phone

	// снимок для интерфейса: читается без s.mu, пока идёт долгий вход
	pub atomic.Pointer[publicInfo]
}

type publicInfo struct {
	appleID string
	phones  []Phone
}

// publish обновляет снимок; вызывается под s.mu.
func (s *Session) publish() {
	s.pub.Store(&publicInfo{appleID: s.st.AppleID, phones: append([]Phone(nil), s.phones...)})
}

// NewSession загружает сохранённую сессию из path (если файл есть).
func NewSession(path string) *Session {
	s := &Session{AuthURL: defaultAuthURL, SetupURL: defaultSetupURL, path: path}
	if data, err := os.ReadFile(path); err == nil {
		_ = json.Unmarshal(data, &s.st)
	}
	if s.st.ClientID == "" {
		s.st.ClientID = newClientID()
	}
	s.jar = NewJar(s.st.Cookies)
	s.publish()
	tr := http.DefaultTransport.(*http.Transport).Clone()
	tr.ResponseHeaderTimeout = 90 * time.Second
	tr.MaxIdleConnsPerHost = 16
	// без общего таймаута: большие видео качаются долго
	s.http = &http.Client{Jar: s.jar, Transport: tr}
	return s
}

// HTTP — клиент с cookies сессии (для CloudKit и загрузок).
func (s *Session) HTTP() *http.Client { return s.http }

func (s *Session) AppleID() string { return s.pub.Load().appleID }

// Account возвращает данные аккаунта после успешного входа, иначе nil.
func (s *Session) Account() *Account {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.account == nil || s.account.needs2FA {
		return nil
	}
	a := *s.account
	return &a
}

// Restore пытается поднять сохранённую сессию без пароля.
func (s *Session) Restore(ctx context.Context) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if len(s.st.Cookies) == 0 && s.st.SessionToken == "" {
		return ErrNeedLogin
	}
	if acc, err := s.setupCall(ctx, "validate", nil); err == nil && acc.PhotosURL != "" {
		acc.AppleID = s.st.AppleID
		s.account = acc
		return s.save()
	}
	if s.st.SessionToken == "" {
		return ErrNeedLogin
	}
	if err := s.accountLogin(ctx); err != nil {
		return ErrNeedLogin
	}
	if s.account.needs2FA {
		return ErrNeedLogin
	}
	return nil
}

// Login выполняет вход. Возвращает ErrNeed2FA, если нужен код.
func (s *Session) Login(ctx context.Context, appleID, password string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	appleID = strings.TrimSpace(appleID)
	if s.st.AppleID != "" && !strings.EqualFold(s.st.AppleID, appleID) {
		// другой аккаунт — старые токены не годятся
		s.st = persisted{ClientID: newClientID()}
		s.jar.Clear()
	}
	s.st.AppleID = appleID
	s.account = nil
	s.phones = nil
	s.publish()

	c, err := newSRPClient(appleID)
	if err != nil {
		return err
	}
	var init struct {
		Iteration int    `json:"iteration"`
		Salt      string `json:"salt"`
		Protocol  string `json:"protocol"`
		B         string `json:"b"`
		C         string `json:"c"`
	}
	resp, err := s.authCall(ctx, http.MethodPost, "/signin/init", nil, map[string]any{
		"a":           base64.StdEncoding.EncodeToString(c.A.Bytes()),
		"accountName": appleID,
		"protocols":   []string{"s2k", "s2k_fo"},
	})
	if err != nil {
		return err
	}
	if resp.status != 200 {
		return authFailure(resp, "Apple не приняла Apple ID")
	}
	if err := json.Unmarshal(resp.body, &init); err != nil {
		return fmt.Errorf("signin/init: %w", err)
	}
	salt, err1 := base64.StdEncoding.DecodeString(init.Salt)
	b, err2 := base64.StdEncoding.DecodeString(init.B)
	if err := errors.Join(err1, err2); err != nil {
		return fmt.Errorf("signin/init: %w", err)
	}
	derived, err := srpPassword(password, init.Protocol, salt, init.Iteration)
	if err != nil {
		return err
	}
	if err := c.process(derived, salt, b); err != nil {
		return err
	}
	trust := []string{}
	if s.st.TrustToken != "" {
		trust = []string{s.st.TrustToken}
	}
	resp, err = s.authCall(ctx, http.MethodPost, "/signin/complete?isRememberMeEnabled=true", nil, map[string]any{
		"accountName": appleID,
		"c":           init.C,
		"m1":          base64.StdEncoding.EncodeToString(c.M1),
		"m2":          base64.StdEncoding.EncodeToString(c.HAMK),
		"rememberMe":  true,
		"trustTokens": trust,
	})
	if err != nil {
		return err
	}
	switch resp.status {
	case 200, 409: // 409 — пароль верный, нужен второй фактор
	case 401:
		return &AuthError{"Неверный Apple ID или пароль. Нужен основной пароль Apple ID — пароли приложений iCloud Photos не принимает."}
	case 403:
		return authFailure(resp, "Apple отклонила вход (аккаунт заблокирован или требуется действие на appleid.apple.com)")
	case 412:
		return &AuthError{"Apple требует подтвердить условия/настройки аккаунта: войдите один раз на icloud.com в браузере и повторите."}
	default:
		return authFailure(resp, "Ошибка входа")
	}
	if err := s.accountLogin(ctx); err != nil {
		return err
	}
	if resp.status == 409 || s.account.needs2FA {
		s.account.needs2FA = true
		s.requestCodePush(ctx)
		_ = s.save()
		return ErrNeed2FA
	}
	return s.save()
}

// Phones — доверенные номера (заполняется после ErrNeed2FA).
// Phones — доверенные номера; всегда не nil (в JSON — [], а не null).
func (s *Session) Phones() []Phone { return append([]Phone{}, s.pub.Load().phones...) }

// SendSMS просит Apple прислать код на доверенный номер.
func (s *Session) SendSMS(ctx context.Context, phoneID int) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	resp, err := s.authCall(ctx, http.MethodPut, "/verify/phone", nil, map[string]any{
		"phoneNumber": map[string]any{"id": phoneID},
		"mode":        "sms",
	})
	if err != nil {
		return err
	}
	if resp.status >= 300 {
		return authFailure(resp, "Не удалось отправить SMS")
	}
	return nil
}

// Verify2FA подтверждает код: phoneID == 0 — код с доверенного устройства, иначе из SMS.
func (s *Session) Verify2FA(ctx context.Context, code string, phoneID int) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	code = strings.TrimSpace(code)
	var resp *apiResp
	var err error
	if phoneID == 0 {
		resp, err = s.authCall(ctx, http.MethodPost, "/verify/trusteddevice/securitycode", nil,
			map[string]any{"securityCode": map[string]any{"code": code}})
	} else {
		resp, err = s.authCall(ctx, http.MethodPost, "/verify/phone/securitycode", nil, map[string]any{
			"phoneNumber":  map[string]any{"id": phoneID},
			"securityCode": map[string]any{"code": code},
			"mode":         "sms",
		})
	}
	if err != nil {
		return err
	}
	if resp.status >= 300 {
		if resp.status == 400 || resp.status == 401 {
			return &AuthError{"Неверный код"}
		}
		return authFailure(resp, "Код не принят")
	}
	// доверяем этому «браузеру», чтобы следующий вход прошёл без кода
	if resp, err = s.authCall(ctx, http.MethodGet, "/2sv/trust", nil, nil); err != nil {
		return err
	} else if resp.status >= 300 {
		return authFailure(resp, "Не удалось сохранить доверие к сессии")
	}
	if err := s.accountLogin(ctx); err != nil {
		return err
	}
	if s.account.needs2FA {
		return &AuthError{"Apple всё ещё требует второй фактор — попробуйте войти заново"}
	}
	return s.save()
}

// Logout забывает сессию и удаляет файл с токенами.
func (s *Session) Logout() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.st = persisted{ClientID: newClientID()}
	s.jar.Clear()
	s.account = nil
	s.phones = nil
	s.publish()
	if err := os.Remove(s.path); err != nil && !os.IsNotExist(err) {
		return err
	}
	return nil
}

// Revalidate проверяет, жива ли сессия (после 401 от CloudKit).
func (s *Session) Revalidate(ctx context.Context) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	acc, err := s.setupCall(ctx, "validate", nil)
	if err != nil || acc.PhotosURL == "" {
		s.account = nil
		return ErrNeedLogin
	}
	acc.AppleID = s.st.AppleID
	s.account = acc
	return s.save()
}

func (s *Session) accountLogin(ctx context.Context) error {
	acc, err := s.setupCall(ctx, "accountLogin", map[string]any{
		"accountCountryCode": s.st.AccountCountry,
		"dsWebAuthToken":     s.st.SessionToken,
		"extended_login":     true,
		"trustToken":         s.st.TrustToken,
	})
	if err != nil {
		return err
	}
	acc.AppleID = s.st.AppleID
	s.account = acc
	return nil
}

func (s *Session) requestCodePush(ctx context.Context) {
	// GET /appleauth/auth возвращает способы подтверждения: доверенные устройства и номера для SMS
	resp, err := s.authCall(ctx, http.MethodGet, "", nil, nil)
	if err != nil || resp.status >= 300 {
		return
	}
	phones, summary := parseAuthOptions(resp.body)
	Logf("2fa options: %s", summary)
	s.phones = phones
	s.publish()
}

// parseAuthOptions достаёт номера для SMS (в двух известных форматах ответа) и
// краткое описание ответа для журнала — только имена полей и количества, без номеров.
func parseAuthOptions(body []byte) ([]Phone, string) {
	var raw map[string]json.RawMessage
	if json.Unmarshal(body, &raw) != nil {
		return nil, fmt.Sprintf("не JSON (%d байт)", len(body))
	}
	var opts struct {
		TrustedPhoneNumbers     []Phone `json:"trustedPhoneNumbers"`
		PhoneNumberVerification struct {
			TrustedPhoneNumbers []Phone `json:"trustedPhoneNumbers"`
		} `json:"phoneNumberVerification"`
		NoTrustedDevices   bool   `json:"noTrustedDevices"`
		AuthenticationType string `json:"authenticationType"`
	}
	_ = json.Unmarshal(body, &opts)
	phones := opts.TrustedPhoneNumbers
	if len(phones) == 0 {
		phones = opts.PhoneNumberVerification.TrustedPhoneNumbers
	}
	keys := make([]string, 0, len(raw))
	for k := range raw {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return phones, fmt.Sprintf("type=%s noTrustedDevices=%v phones=%d keys=%v",
		opts.AuthenticationType, opts.NoTrustedDevices, len(phones), keys)
}

// ResendCode просит Apple ещё раз отправить код на доверенные устройства.
func (s *Session) ResendCode(ctx context.Context) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	resp, err := s.authCall(ctx, http.MethodPut, "/verify/trusteddevice/securitycode", nil, nil)
	if err != nil {
		return err
	}
	if resp.status >= 300 {
		return authFailure(resp, "Apple не отправила код")
	}
	if phones, summary := parseAuthOptions(resp.body); len(phones) > 0 {
		Logf("2fa resend: %s", summary)
		s.phones = phones
		s.publish()
	}
	return nil
}

func (s *Session) setupCall(ctx context.Context, path string, body any) (*Account, error) {
	u := s.SetupURL + "/" + path + "?" + s.clientParams().Encode()
	var rd io.Reader = strings.NewReader("null")
	if body != nil {
		b, _ := json.Marshal(body)
		rd = bytes.NewReader(b)
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, u, rd)
	if err != nil {
		return nil, err
	}
	s.webHeaders(req)
	resp, err := s.do(req)
	if err != nil {
		return nil, err
	}
	if resp.status >= 300 {
		return nil, authFailure(resp, "iCloud отклонил сессию")
	}
	var data struct {
		DsInfo struct {
			DSID       string `json:"dsid"`
			FullName   string `json:"fullName"`
			HsaVersion int    `json:"hsaVersion"`
		} `json:"dsInfo"`
		Webservices map[string]struct {
			URL    string `json:"url"`
			Status string `json:"status"`
		} `json:"webservices"`
		HsaChallengeRequired bool `json:"hsaChallengeRequired"`
		HsaTrustedBrowser    bool `json:"hsaTrustedBrowser"`
	}
	if err := json.Unmarshal(resp.body, &data); err != nil {
		return nil, fmt.Errorf("%s: %w", path, err)
	}
	return &Account{
		DSID:       data.DsInfo.DSID,
		FullName:   data.DsInfo.FullName,
		PhotosURL:  data.Webservices["ckdatabasews"].URL,
		hsaVersion: data.DsInfo.HsaVersion,
		needs2FA:   data.DsInfo.HsaVersion == 2 && (data.HsaChallengeRequired || !data.HsaTrustedBrowser),
	}, nil
}

type apiResp struct {
	status int
	body   []byte
}

func (s *Session) authCall(ctx context.Context, method, path string, query url.Values, body any) (*apiResp, error) {
	u := s.AuthURL + path
	if len(query) > 0 {
		u += "?" + query.Encode()
	}
	var rd io.Reader
	if body != nil {
		b, _ := json.Marshal(body)
		rd = bytes.NewReader(b)
	}
	req, err := http.NewRequestWithContext(ctx, method, u, rd)
	if err != nil {
		return nil, err
	}
	s.webHeaders(req)
	h := req.Header
	h.Set("Accept", "application/json, text/javascript")
	h.Set("X-Apple-OAuth-Client-Id", widgetKey)
	h.Set("X-Apple-OAuth-Client-Type", "firstPartyAuth")
	h.Set("X-Apple-OAuth-Redirect-URI", homeURL)
	h.Set("X-Apple-OAuth-Require-Grant-Code", "true")
	h.Set("X-Apple-OAuth-Response-Mode", "web_message")
	h.Set("X-Apple-OAuth-Response-Type", "code")
	h.Set("X-Apple-OAuth-State", s.st.ClientID)
	h.Set("X-Apple-Widget-Key", widgetKey)
	if s.st.SessionID != "" {
		h.Set("X-Apple-ID-Session-Id", s.st.SessionID)
	}
	if s.st.Scnt != "" {
		h.Set("scnt", s.st.Scnt)
	}
	return s.do(req)
}

func (s *Session) webHeaders(req *http.Request) {
	req.Header.Set("Origin", homeURL)
	req.Header.Set("Referer", homeURL+"/")
	req.Header.Set("User-Agent", userAgent)
	req.Header.Set("Content-Type", "application/json")
}

func (s *Session) do(req *http.Request) (*apiResp, error) {
	start := time.Now()
	resp, err := s.http.Do(req)
	if err != nil {
		Logf("apple %s %s%s: %v (%s)", req.Method, req.URL.Host, req.URL.Path, err, time.Since(start).Round(time.Millisecond))
		return nil, err
	}
	Logf("apple %s %s%s → %d (%s)", req.Method, req.URL.Host, req.URL.Path, resp.StatusCode, time.Since(start).Round(time.Millisecond))
	defer resp.Body.Close()
	body, err := io.ReadAll(io.LimitReader(resp.Body, 16<<20))
	if err != nil {
		return nil, err
	}
	for header, dst := range map[string]*string{
		"X-Apple-ID-Account-Country": &s.st.AccountCountry,
		"X-Apple-ID-Session-Id":      &s.st.SessionID,
		"X-Apple-Session-Token":      &s.st.SessionToken,
		"X-Apple-TwoSV-Trust-Token":  &s.st.TrustToken,
		"scnt":                       &s.st.Scnt,
	} {
		if v := resp.Header.Get(header); v != "" {
			*dst = v
		}
	}
	return &apiResp{status: resp.StatusCode, body: body}, nil
}

func (s *Session) clientParams() url.Values {
	v := url.Values{}
	v.Set("clientBuildNumber", "2534Project66")
	v.Set("clientMasteringNumber", "2534B22")
	v.Set("clientId", s.st.ClientID)
	if s.account != nil && s.account.DSID != "" {
		v.Set("dsid", s.account.DSID)
	}
	return v
}

// ClientParams — общие параметры запросов к сервисам iCloud.
func (s *Session) ClientParams() url.Values {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.clientParams()
}

// WebHeaders выставляет заголовки, которые ждут сервисы icloud.com.
func (s *Session) WebHeaders(req *http.Request) { s.webHeaders(req) }

func (s *Session) save() error {
	s.st.Cookies = s.jar.Export()
	data, err := json.MarshalIndent(s.st, "", "  ")
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(s.path), 0o700); err != nil {
		return err
	}
	tmp := s.path + ".tmp"
	if err := os.WriteFile(tmp, data, 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, s.path)
}

// Save сохраняет cookies после работы сервисов (они могут обновляться).
func (s *Session) Save() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.account == nil {
		return nil
	}
	return s.save()
}

func authFailure(r *apiResp, prefix string) error {
	var body struct {
		ServiceErrors []struct {
			Code    string `json:"code"`
			Message string `json:"message"`
		} `json:"serviceErrors"`
		Error  string `json:"error"`
		Reason string `json:"reason"`
	}
	msg := ""
	if json.Unmarshal(r.body, &body) == nil {
		switch {
		case len(body.ServiceErrors) > 0:
			msg = body.ServiceErrors[0].Message
		case body.Reason != "":
			msg = body.Reason
		case body.Error != "":
			msg = body.Error
		}
	}
	if msg == "" {
		return &AuthError{fmt.Sprintf("%s (HTTP %d)", prefix, r.status)}
	}
	return &AuthError{fmt.Sprintf("%s: %s", prefix, msg)}
}

func newClientID() string { return "auth-" + newUUID() }

func newUUID() string {
	var b [16]byte
	_, _ = rand.Read(b[:])
	b[6] = b[6]&0x0f | 0x40
	b[8] = b[8]&0x3f | 0x80
	return fmt.Sprintf("%x-%x-%x-%x-%x", b[0:4], b[4:6], b[6:8], b[8:10], b[10:16])
}
