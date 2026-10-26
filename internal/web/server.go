// Package web — локальный HTTP API для интерфейса и раздача встроенного фронтенда.
package web

import (
	"bufio"
	"context"
	"crypto/rand"
	"crypto/subtle"
	"embed"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"log"
	"net"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/rusjoan/icloud-export-app-macos/internal/export"
	"github.com/rusjoan/icloud-export-app-macos/internal/icloud"
	"github.com/rusjoan/icloud-export-app-macos/internal/meta"
)

//go:embed all:dist
var dist embed.FS

// FolderPicker открывает нативный диалог выбора папки ("" — отменено).
type FolderPicker func() (string, error)

type Server struct {
	Version string
	Picker  FolderPicker // nil — в браузерном режиме

	sess  *icloud.Session
	token string
	ctx   context.Context

	mu         sync.Mutex
	authState  string // none | 2fa | ok | restoring
	photos     *icloud.Photos
	albums     []icloud.Album
	engine     *export.Engine
	exifPath   string
	exifVer    string
	installing bool
	installLog []string
	installErr string
}

func New(ctx context.Context, sess *icloud.Session) *Server {
	b := make([]byte, 16)
	_, _ = rand.Read(b)
	s := &Server{sess: sess, token: hex.EncodeToString(b), ctx: ctx, authState: "restoring"}
	s.detectExifTool()
	go func() {
		err := sess.Restore(ctx)
		s.mu.Lock()
		defer s.mu.Unlock()
		if err == nil {
			s.authState = "ok"
		} else {
			s.authState = "none"
		}
	}()
	return s
}

// Listen поднимает сервер на 127.0.0.1:<свободный порт> и возвращает URL интерфейса.
func (s *Server) Listen() (string, error) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return "", err
	}
	addr := ln.Addr().String()
	srv := &http.Server{Handler: s.handler(addr), ReadHeaderTimeout: 10 * time.Second}
	go func() {
		if err := srv.Serve(ln); err != nil && !errors.Is(err, http.ErrServerClosed) {
			log.Println("http:", err)
		}
	}()
	go func() {
		<-s.ctx.Done()
		s.shutdown()
		_ = srv.Close()
	}()
	return "http://" + addr + "/?t=" + s.token, nil
}

// Close останавливает выгрузку (индекс при этом сохраняется).
func (s *Server) Close() { s.shutdown() }

func (s *Server) shutdown() {
	s.mu.Lock()
	e := s.engine
	s.mu.Unlock()
	if e != nil {
		e.Cancel()
		select {
		case <-e.Done():
		case <-time.After(5 * time.Second):
		}
	}
}

func (s *Server) handler(addr string) http.Handler {
	mux := http.NewServeMux()
	api := func(pattern string, h func(w http.ResponseWriter, r *http.Request) (any, error)) {
		mux.HandleFunc(pattern, func(w http.ResponseWriter, r *http.Request) {
			if !s.authorized(r) {
				http.Error(w, "forbidden", http.StatusForbidden)
				return
			}
			out, err := h(w, r)
			if err != nil {
				writeErr(w, err)
				return
			}
			if out != nil {
				w.Header().Set("Content-Type", "application/json")
				_ = json.NewEncoder(w).Encode(out)
			}
		})
	}
	api("GET /api/state", func(w http.ResponseWriter, r *http.Request) (any, error) { return s.state(), nil })
	api("GET /api/events", s.events)
	api("POST /api/login", s.login)
	api("POST /api/2fa", s.verify2FA)
	api("POST /api/2fa/sms", s.sendSMS)
	api("POST /api/logout", s.logout)
	api("GET /api/albums", s.listAlbums)
	api("POST /api/pick-folder", s.pickFolder)
	api("GET /api/dest", s.destInfo)
	api("POST /api/export", s.startExport)
	api("POST /api/export/{action}", s.controlExport)
	api("POST /api/exiftool/install", s.installExifTool)
	api("POST /api/reveal", s.reveal)
	api("POST /api/open-url", s.openURL)

	sub, _ := fs.Sub(dist, "dist")
	files := http.FileServerFS(sub)
	mux.Handle("GET /", http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Cache-Control", "no-store")
		files.ServeHTTP(w, r)
	}))

	allowed := map[string]bool{addr: true, strings.Replace(addr, "127.0.0.1", "localhost", 1): true}
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// защита от DNS rebinding: только наш адрес
		if !allowed[r.Host] {
			http.Error(w, "bad host", http.StatusForbidden)
			return
		}
		if !strings.HasPrefix(r.URL.Path, "/api/") || r.URL.Path == "/api/events" || r.URL.Path == "/api/state" {
			mux.ServeHTTP(w, r)
			return
		}
		start := time.Now()
		rec := &statusRecorder{ResponseWriter: w, status: 200}
		log.Printf("ui %s %s …", r.Method, r.URL.Path)
		mux.ServeHTTP(rec, r)
		log.Printf("ui %s %s → %d (%s)", r.Method, r.URL.Path, rec.status, time.Since(start).Round(time.Millisecond))
	})
}

type statusRecorder struct {
	http.ResponseWriter
	status int
}

func (r *statusRecorder) WriteHeader(code int) {
	r.status = code
	r.ResponseWriter.WriteHeader(code)
}

// authTimeout — сколько ждём Apple при входе, прежде чем показать ошибку.
const authTimeout = 90 * time.Second

func (s *Server) authorized(r *http.Request) bool {
	t := r.Header.Get("X-Token")
	if t == "" {
		t = r.URL.Query().Get("t") // EventSource не умеет заголовки
	}
	return subtle.ConstantTimeCompare([]byte(t), []byte(s.token)) == 1
}

type httpError struct {
	code int
	msg  string
}

func (e *httpError) Error() string { return e.msg }

func badRequest(msg string) error { return &httpError{http.StatusBadRequest, msg} }

func writeErr(w http.ResponseWriter, err error) {
	code := http.StatusInternalServerError
	var he *httpError
	var ae *icloud.AuthError
	switch {
	case errors.As(err, &he):
		code = he.code
	case errors.As(err, &ae):
		code = http.StatusUnauthorized
	case errors.Is(err, icloud.ErrNeedLogin):
		code = http.StatusUnauthorized
	}
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)
	_ = json.NewEncoder(w).Encode(map[string]string{"error": err.Error()})
}

func decode(r *http.Request, v any) error {
	if err := json.NewDecoder(http.MaxBytesReader(nil, r.Body, 1<<20)).Decode(v); err != nil {
		return badRequest("некорректный запрос: " + err.Error())
	}
	return nil
}

type exifState struct {
	Path       string   `json:"path"`
	Version    string   `json:"version"`
	Brew       bool     `json:"brew"`
	Installing bool     `json:"installing"`
	Log        []string `json:"log"`
	Error      string   `json:"error,omitempty"`
}

type stateDTO struct {
	Version      string           `json:"version"`
	Auth         string           `json:"auth"`
	AppleID      string           `json:"appleId"`
	Phones       []icloud.Phone   `json:"phones"`
	ExifTool     exifState        `json:"exiftool"`
	Export       *export.Progress `json:"export"`
	NativePicker bool             `json:"nativePicker"`
}

func (s *Server) state() stateDTO {
	s.mu.Lock()
	defer s.mu.Unlock()
	st := stateDTO{
		Version: s.Version, Auth: s.authState, AppleID: s.sess.AppleID(), NativePicker: s.Picker != nil,
		ExifTool: exifState{Path: s.exifPath, Version: s.exifVer, Brew: meta.FindTool("brew") != "",
			Installing: s.installing, Log: append([]string{}, s.installLog...), Error: s.installErr},
	}
	if s.authState == "2fa" {
		st.Phones = s.sess.Phones()
	}
	if s.engine != nil && s.authState == "ok" && s.engine.Progress().Phase == export.PhaseNeedLogin {
		st.Auth = "none"
	}
	if s.engine != nil {
		p := s.engine.Progress()
		st.Export = &p
	}
	return st
}

// reveal открывает папку текущей выгрузки в Finder.
func (s *Server) reveal(w http.ResponseWriter, r *http.Request) (any, error) {
	s.mu.Lock()
	e := s.engine
	s.mu.Unlock()
	if e == nil {
		return nil, badRequest("Выгрузка не запускалась")
	}
	return map[string]bool{"ok": true}, exec.Command("open", e.Progress().Dest).Run()
}

// openURL открывает ссылку в браузере по умолчанию (WKWebView не открывает target=_blank).
func (s *Server) openURL(w http.ResponseWriter, r *http.Request) (any, error) {
	var req struct{ URL string }
	if err := decode(r, &req); err != nil {
		return nil, err
	}
	u, err := url.Parse(req.URL)
	if err != nil || u.Scheme != "https" || !allowedHost(u.Hostname()) {
		return nil, badRequest("ссылка не разрешена")
	}
	return map[string]bool{"ok": true}, exec.Command("open", u.String()).Run()
}

func allowedHost(h string) bool {
	for _, d := range []string{"apple.com", "brew.sh"} {
		if h == d || strings.HasSuffix(h, "."+d) {
			return true
		}
	}
	return false
}

// events — SSE: раз в 700 мс полное состояние (его немного, так проще и надёжнее).
func (s *Server) events(w http.ResponseWriter, r *http.Request) (any, error) {
	fl, ok := w.(http.Flusher)
	if !ok {
		return nil, errors.New("streaming unsupported")
	}
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-store")
	tick := time.NewTicker(700 * time.Millisecond)
	defer tick.Stop()
	for {
		data, _ := json.Marshal(s.state())
		if _, err := fmt.Fprintf(w, "data: %s\n\n", data); err != nil {
			return nil, nil
		}
		fl.Flush()
		select {
		case <-r.Context().Done():
			return nil, nil
		case <-tick.C:
		}
	}
}

func (s *Server) login(w http.ResponseWriter, r *http.Request) (any, error) {
	var req struct{ AppleID, Password string }
	if err := decode(r, &req); err != nil {
		return nil, err
	}
	if strings.TrimSpace(req.AppleID) == "" || req.Password == "" {
		return nil, badRequest("Введите Apple ID и пароль")
	}
	ctx, cancel := context.WithTimeout(r.Context(), authTimeout)
	defer cancel()
	err := s.sess.Login(ctx, req.AppleID, req.Password)
	if ctx.Err() == context.DeadlineExceeded {
		err = &icloud.AuthError{Msg: "Apple не ответила за 90 секунд, попробуйте ещё раз"}
	}
	log.Printf("login: %v", errOrOK(err))
	s.mu.Lock()
	defer s.mu.Unlock()
	switch {
	case errors.Is(err, icloud.ErrNeed2FA):
		s.authState = "2fa"
	case err != nil:
		s.authState = "none"
		return nil, err
	default:
		s.loggedInLocked()
	}
	return map[string]string{"auth": s.authState}, nil
}

func (s *Server) loggedInLocked() {
	s.authState = "ok"
	s.photos = nil // новый клиент с новым URL сервиса
	if s.engine != nil && s.engine.Progress().Phase == export.PhaseNeedLogin {
		s.engine.Resume()
	}
}

func (s *Server) verify2FA(w http.ResponseWriter, r *http.Request) (any, error) {
	var req struct {
		Code    string
		PhoneID int
	}
	if err := decode(r, &req); err != nil {
		return nil, err
	}
	ctx, cancel := context.WithTimeout(r.Context(), authTimeout)
	defer cancel()
	err := s.sess.Verify2FA(ctx, req.Code, req.PhoneID)
	log.Printf("2fa: %v", errOrOK(err))
	if err != nil {
		return nil, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.loggedInLocked()
	return map[string]string{"auth": s.authState}, nil
}

func (s *Server) sendSMS(w http.ResponseWriter, r *http.Request) (any, error) {
	var req struct{ PhoneID int }
	if err := decode(r, &req); err != nil {
		return nil, err
	}
	ctx, cancel := context.WithTimeout(r.Context(), authTimeout)
	defer cancel()
	return map[string]bool{"ok": true}, s.sess.SendSMS(ctx, req.PhoneID)
}

func errOrOK(err error) string {
	if err == nil {
		return "ok"
	}
	return err.Error()
}

func (s *Server) logout(w http.ResponseWriter, r *http.Request) (any, error) {
	s.mu.Lock()
	if s.engine != nil {
		if ph := s.engine.Progress().Phase; ph != export.PhaseDone && ph != export.PhaseCancelled && ph != export.PhaseFailed {
			s.mu.Unlock()
			return nil, &httpError{http.StatusConflict, "Сначала остановите выгрузку"}
		}
	}
	s.authState, s.photos, s.albums = "none", nil, nil
	s.mu.Unlock()
	return map[string]bool{"ok": true}, s.sess.Logout()
}

func (s *Server) photosClient(ctx context.Context) (*icloud.Photos, error) {
	s.mu.Lock()
	p := s.photos
	s.mu.Unlock()
	if p != nil {
		return p, nil
	}
	p, err := icloud.NewPhotos(ctx, s.sess)
	if err != nil {
		if errors.Is(err, icloud.ErrNeedLogin) {
			s.mu.Lock()
			s.authState = "none"
			s.mu.Unlock()
		}
		return nil, err
	}
	s.mu.Lock()
	s.photos = p
	s.mu.Unlock()
	return p, nil
}

func (s *Server) listAlbums(w http.ResponseWriter, r *http.Request) (any, error) {
	s.mu.Lock()
	cached := s.albums
	s.mu.Unlock()
	if cached != nil && r.URL.Query().Get("refresh") == "" {
		return cached, nil
	}
	p, err := s.photosClient(r.Context())
	if err != nil {
		return nil, err
	}
	albums, err := p.Albums(r.Context())
	if err != nil {
		return nil, err
	}
	s.mu.Lock()
	s.albums = albums
	s.mu.Unlock()
	return albums, nil
}

func (s *Server) pickFolder(w http.ResponseWriter, r *http.Request) (any, error) {
	if s.Picker == nil {
		return nil, &httpError{http.StatusNotImplemented, "Выбор папки доступен только в окне приложения — введите путь вручную"}
	}
	p, err := s.Picker()
	if err != nil {
		return nil, err
	}
	return map[string]string{"path": p}, nil
}

func expandPath(p string) (string, error) {
	p = strings.TrimSpace(p)
	if strings.HasPrefix(p, "~/") || p == "~" {
		home, _ := os.UserHomeDir()
		p = filepath.Join(home, strings.TrimPrefix(p, "~"))
	}
	if !filepath.IsAbs(p) {
		return "", badRequest("Укажите полный путь к папке")
	}
	return filepath.Clean(p), nil
}

func (s *Server) destInfo(w http.ResponseWriter, r *http.Request) (any, error) {
	p, err := expandPath(r.URL.Query().Get("path"))
	if err != nil {
		return nil, err
	}
	fi, statErr := os.Stat(p)
	if statErr == nil && !fi.IsDir() {
		return nil, badRequest("Это файл, а не папка")
	}
	return map[string]any{"path": p, "exists": statErr == nil, "layout": export.LayoutOf(p)}, nil
}

func (s *Server) startExport(w http.ResponseWriter, r *http.Request) (any, error) {
	var req struct {
		Albums      []string
		Dest        string
		Layout      string
		Concurrency int
	}
	if err := decode(r, &req); err != nil {
		return nil, err
	}
	dest, err := expandPath(req.Dest)
	if err != nil {
		return nil, err
	}
	s.mu.Lock()
	if s.engine != nil {
		if ph := s.engine.Progress().Phase; ph != export.PhaseDone && ph != export.PhaseCancelled && ph != export.PhaseFailed {
			s.mu.Unlock()
			return nil, &httpError{http.StatusConflict, "Выгрузка уже идёт"}
		}
	}
	byID := map[string]icloud.Album{}
	for _, a := range s.albums {
		byID[a.ID] = a
	}
	exifPath := s.exifPath
	s.mu.Unlock()

	var albums []icloud.Album
	for _, id := range req.Albums {
		if a, ok := byID[id]; ok && a.Kind != icloud.KindFolder {
			albums = append(albums, a)
		}
	}
	if len(albums) == 0 {
		return nil, badRequest("Выберите хотя бы один альбом")
	}
	p, err := s.photosClient(r.Context())
	if err != nil {
		return nil, err
	}

	var et *meta.ExifTool
	if exifPath != "" {
		if et, err = meta.StartExifTool(exifPath); err != nil {
			et = nil // без exiftool — сайдкары
		}
	}
	e, err := export.Start(s.ctx, export.Options{
		Dest: dest, Layout: export.Layout(req.Layout), Concurrency: req.Concurrency, Albums: albums,
	}, p, meta.NewWriter(et), s.sess.HTTP())
	if err != nil {
		if et != nil {
			_ = et.Close()
		}
		return nil, err
	}
	go func() {
		<-e.Done()
		if et != nil {
			_ = et.Close()
		}
		_ = s.sess.Save()
	}()
	s.mu.Lock()
	s.engine = e
	s.mu.Unlock()
	return map[string]bool{"ok": true}, nil
}

func (s *Server) controlExport(w http.ResponseWriter, r *http.Request) (any, error) {
	s.mu.Lock()
	e := s.engine
	s.mu.Unlock()
	if e == nil {
		return nil, badRequest("Выгрузка не запущена")
	}
	switch r.PathValue("action") {
	case "pause":
		e.Pause()
	case "resume":
		e.Resume()
	case "cancel":
		e.Cancel()
	case "clear":
		select {
		case <-e.Done():
			s.mu.Lock()
			s.engine = nil
			s.mu.Unlock()
		default:
			return nil, &httpError{http.StatusConflict, "Выгрузка ещё идёт"}
		}
	default:
		return nil, badRequest("неизвестное действие")
	}
	return map[string]bool{"ok": true}, nil
}

func (s *Server) detectExifTool() {
	p := meta.FindTool("exiftool")
	v := ""
	if p != "" {
		if v = meta.ExifToolVersion(p); v == "" {
			p = ""
		}
	}
	s.mu.Lock()
	s.exifPath, s.exifVer = p, v
	s.mu.Unlock()
}

func (s *Server) installExifTool(w http.ResponseWriter, r *http.Request) (any, error) {
	brew := meta.FindTool("brew")
	if brew == "" {
		return nil, badRequest("Homebrew не найден")
	}
	s.mu.Lock()
	if s.installing {
		s.mu.Unlock()
		return nil, &httpError{http.StatusConflict, "Установка уже идёт"}
	}
	s.installing, s.installLog, s.installErr = true, nil, ""
	s.mu.Unlock()

	go func() {
		cmd := exec.CommandContext(s.ctx, brew, "install", "exiftool")
		cmd.Env = append(os.Environ(), "PATH="+filepath.Dir(brew)+":/usr/bin:/bin:/usr/sbin:/sbin", "HOMEBREW_NO_ENV_HINTS=1")
		out, err := cmd.StdoutPipe()
		if err == nil {
			cmd.Stderr = cmd.Stdout
			err = cmd.Start()
		}
		if err == nil {
			sc := bufio.NewScanner(out)
			for sc.Scan() {
				s.mu.Lock()
				s.installLog = append(s.installLog, sc.Text())
				if len(s.installLog) > 300 {
					s.installLog = s.installLog[len(s.installLog)-300:]
				}
				s.mu.Unlock()
			}
			err = cmd.Wait()
		}
		s.detectExifTool()
		s.mu.Lock()
		s.installing = false
		if err != nil {
			s.installErr = "brew install exiftool: " + err.Error()
		} else if s.exifPath == "" {
			s.installErr = "exiftool установлен, но не найден"
		}
		s.mu.Unlock()
	}()
	return map[string]bool{"ok": true}, nil
}
