// Package export — выгрузка ассетов iCloud в папку с докачкой.
package export

import (
	"context"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"math/rand/v2"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"slices"
	"sort"
	"strings"
	"sync"
	"time"

	"icloud-export/internal/icloud"
	"icloud-export/internal/meta"
	"icloud-export/internal/state"
)

const (
	partSuffix = ".icloud-export.part"
	errLogName = ".icloud-export-errors.log"
	maxErrors  = 200
)

// Source — откуда берём ассеты (icloud.Photos или фейк в тестах).
type Source interface {
	Assets(ctx context.Context, album icloud.Album, fn func(icloud.Asset) error) error
	Refresh(ctx context.Context, a icloud.Asset) (icloud.Asset, error)
}

type Options struct {
	Dest        string
	Layout      Layout
	Concurrency int
	Albums      []icloud.Album
	// RetryDelay — пауза перед повтором загрузки (nil — экспоненциальная от 1 с).
	RetryDelay func(attempt int) time.Duration
}

type Phase string

const (
	PhaseScan      Phase = "scan"
	PhaseDownload  Phase = "download"
	PhasePaused    Phase = "paused"
	PhaseNeedLogin Phase = "need_login"
	PhaseDone      Phase = "done"
	PhaseCancelled Phase = "cancelled"
	PhaseFailed    Phase = "failed"
)

type Progress struct {
	Phase      Phase     `json:"phase"`
	Scanned    int       `json:"scanned"`
	Total      int       `json:"total"`
	Done       int       `json:"done"`
	Downloaded int       `json:"downloaded"`
	Skipped    int       `json:"skipped"`
	Failed     int       `json:"failed"`
	Bytes      int64     `json:"bytes"`
	TotalBytes int64     `json:"totalBytes"`
	Current    []string  `json:"current"`
	Errors     []string  `json:"errors"`
	Message    string    `json:"message,omitempty"`
	Started    time.Time `json:"started"`
	Finished   time.Time `json:"finished,omitzero"`
	Dest       string    `json:"dest"`
}

// Engine — одна выгрузка.
type Engine struct {
	opts   Options
	src    Source
	writer *meta.Writer
	httpc  *http.Client
	index  *state.Index
	errLog *os.File

	cancel context.CancelFunc
	done   chan struct{}

	mu      sync.Mutex
	cond    *sync.Cond
	paused  bool
	prog    Progress
	current map[int]string

	retryDelay func(attempt int) time.Duration
}

// Start проверяет папку и запускает выгрузку в фоне.
func Start(parent context.Context, opts Options, src Source, writer *meta.Writer, httpc *http.Client) (*Engine, error) {
	if opts.Concurrency < 1 || opts.Concurrency > 16 {
		opts.Concurrency = 4
	}
	if opts.Layout != LayoutDate {
		opts.Layout = LayoutFlat
	}
	if err := os.MkdirAll(opts.Dest, 0o755); err != nil {
		return nil, err
	}
	ix, err := state.Open(opts.Dest)
	if err != nil {
		return nil, err
	}
	if ix.Layout() == "" {
		ix.SetLayout(string(opts.Layout))
	}
	errLog, err := os.OpenFile(filepath.Join(opts.Dest, errLogName), os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o644)
	if err != nil {
		return nil, err
	}
	ctx, cancel := context.WithCancel(parent)
	e := &Engine{
		opts: opts, src: src, writer: writer, httpc: httpc, index: ix, errLog: errLog,
		cancel: cancel, done: make(chan struct{}), current: map[int]string{},
		prog:       Progress{Phase: PhaseScan, Started: time.Now(), Dest: opts.Dest},
		retryDelay: opts.RetryDelay,
	}
	if e.retryDelay == nil {
		e.retryDelay = func(n int) time.Duration {
			return time.Duration(1<<n)*time.Second + time.Duration(rand.IntN(500))*time.Millisecond
		}
	}
	e.cond = sync.NewCond(&e.mu)
	go e.run(ctx)
	return e, nil
}

// LayoutOf — раскладка существующей выгрузки в dir ("" — выгрузки нет).
func LayoutOf(dir string) string {
	ix, err := state.Open(dir)
	if err != nil || ix.Len() == 0 {
		return ""
	}
	return ix.Layout()
}

func (e *Engine) Done() <-chan struct{} { return e.done }

func (e *Engine) Progress() Progress {
	e.mu.Lock()
	defer e.mu.Unlock()
	p := e.prog
	p.Current = make([]string, 0, len(e.current))
	for _, c := range e.current {
		p.Current = append(p.Current, c)
	}
	sort.Strings(p.Current)
	p.Errors = slices.Clone(e.prog.Errors)
	return p
}

func (e *Engine) Pause() {
	e.mu.Lock()
	if e.prog.Phase == PhaseDownload || e.prog.Phase == PhaseScan {
		e.paused = true
		e.prog.Phase = PhasePaused
	}
	e.mu.Unlock()
}

// Resume продолжает после паузы или повторного входа.
func (e *Engine) Resume() {
	e.mu.Lock()
	if e.paused {
		e.paused = false
		if e.prog.Total > 0 {
			e.prog.Phase = PhaseDownload
		} else {
			e.prog.Phase = PhaseScan
		}
		e.prog.Message = ""
		e.cond.Broadcast()
	}
	e.mu.Unlock()
}

func (e *Engine) Cancel() {
	e.cancel()
	e.mu.Lock()
	e.paused = false
	e.cond.Broadcast()
	e.mu.Unlock()
}

// gate блокирует, пока выгрузка на паузе; false — отменено.
func (e *Engine) gate(ctx context.Context) bool {
	e.mu.Lock()
	defer e.mu.Unlock()
	for e.paused && ctx.Err() == nil {
		e.cond.Wait()
	}
	return ctx.Err() == nil
}

func (e *Engine) needLogin() {
	e.mu.Lock()
	e.paused = true
	e.prog.Phase = PhaseNeedLogin
	e.prog.Message = "Сессия iCloud истекла — войдите снова, выгрузка продолжится"
	e.mu.Unlock()
}

type item struct {
	asset    icloud.Asset
	keywords []string
}

func (e *Engine) run(ctx context.Context) {
	defer close(e.done)
	defer e.errLog.Close()
	e.cleanParts()

	items, err := e.scan(ctx)
	if err == nil {
		e.mu.Lock()
		e.prog.Phase = PhaseDownload
		if e.paused {
			e.prog.Phase = PhasePaused
		}
		e.prog.Total = len(items)
		e.mu.Unlock()
		err = e.download(ctx, items)
	}
	saveErr := e.index.Save()

	e.mu.Lock()
	defer e.mu.Unlock()
	e.prog.Finished = time.Now()
	switch {
	case ctx.Err() != nil:
		e.prog.Phase = PhaseCancelled
	case err != nil:
		e.prog.Phase, e.prog.Message = PhaseFailed, err.Error()
	case saveErr != nil:
		e.prog.Phase, e.prog.Message = PhaseFailed, "не удалось сохранить индекс: "+saveErr.Error()
	default:
		e.prog.Phase = PhaseDone
	}
}

// cleanParts удаляет недокачанные файлы прошлых запусков (только свои *.icloud-export.part).
func (e *Engine) cleanParts() {
	_ = filepath.WalkDir(e.opts.Dest, func(p string, d fs.DirEntry, err error) error {
		if err == nil && !d.IsDir() && strings.HasSuffix(d.Name(), partSuffix) {
			_ = os.Remove(p)
		}
		return nil
	})
}

func (e *Engine) scan(ctx context.Context) ([]*item, error) {
	byID := map[string]*item{}
	var order []*item
	for _, al := range e.opts.Albums {
		for {
			if !e.gate(ctx) {
				return nil, ctx.Err()
			}
			err := e.src.Assets(ctx, al, func(a icloud.Asset) error {
				it, ok := byID[a.ID]
				if !ok {
					it = &item{asset: a}
					byID[a.ID] = it
					order = append(order, it)
					e.mu.Lock()
					e.prog.Scanned = len(order)
					if r := selectResources(a); r.main != nil {
						e.prog.TotalBytes += r.main.Size
						if r.live != nil {
							e.prog.TotalBytes += r.live.Size
						}
					}
					e.mu.Unlock()
				}
				if al.Keyword != "" && !slices.Contains(it.keywords, al.Keyword) {
					it.keywords = append(it.keywords, al.Keyword)
				}
				return ctx.Err()
			})
			if errors.Is(err, icloud.ErrNeedLogin) {
				// повторяем альбом целиком после входа: дубли отсекаются по ID
				e.needLogin()
				continue
			}
			if err != nil {
				return nil, fmt.Errorf("альбом «%s»: %w", al.Name, err)
			}
			break
		}
	}
	for _, it := range order {
		if it.asset.Hidden && !slices.Contains(it.keywords, "Hidden") {
			it.keywords = append(it.keywords, "Hidden")
		}
		if it.asset.Deleted && !slices.Contains(it.keywords, "Recently Deleted") {
			it.keywords = append(it.keywords, "Recently Deleted")
		}
	}
	return order, nil
}

func (e *Engine) download(ctx context.Context, items []*item) error {
	queue := make(chan *item)
	var wg sync.WaitGroup
	for w := 0; w < e.opts.Concurrency; w++ {
		wg.Add(1)
		go func(worker int) {
			defer wg.Done()
			for it := range queue {
				e.processWithLogin(ctx, worker, it)
			}
		}(w)
	}
	for _, it := range items {
		if !e.gate(ctx) {
			break
		}
		select {
		case queue <- it:
		case <-ctx.Done():
		}
	}
	close(queue)
	wg.Wait()
	return nil
}

func (e *Engine) processWithLogin(ctx context.Context, worker int, it *item) {
	for {
		if !e.gate(ctx) {
			return
		}
		e.setCurrent(worker, it.asset.Filename)
		skipped, err := e.process(ctx, it)
		e.setCurrent(worker, "")
		if errors.Is(err, icloud.ErrNeedLogin) {
			e.needLogin()
			continue
		}
		if ctx.Err() != nil {
			return
		}
		e.mu.Lock()
		e.prog.Done++
		switch {
		case err != nil:
			e.prog.Failed++
			msg := fmt.Sprintf("%s: %v", nameOr(it.asset), err)
			if len(e.prog.Errors) < maxErrors {
				e.prog.Errors = append(e.prog.Errors, msg)
			}
			fmt.Fprintf(e.errLog, "%s\t%s\t%s\n", time.Now().Format(time.RFC3339), it.asset.ID, msg)
		case skipped:
			e.prog.Skipped++
		default:
			e.prog.Downloaded++
		}
		e.mu.Unlock()
		return
	}
}

func nameOr(a icloud.Asset) string {
	if a.Filename != "" {
		return a.Filename
	}
	return a.ID
}

func (e *Engine) setCurrent(worker int, name string) {
	e.mu.Lock()
	if name == "" {
		delete(e.current, worker)
	} else {
		e.current[worker] = name
	}
	e.mu.Unlock()
}

func (e *Engine) addBytes(n int64) {
	e.mu.Lock()
	e.prog.Bytes += n
	e.mu.Unlock()
}

// process скачивает один ассет; skipped — уже был скачан.
func (e *Engine) process(ctx context.Context, it *item) (skipped bool, err error) {
	a := it.asset
	sel := selectResources(a)
	if sel.main == nil {
		return false, errors.New("у записи нет файла для загрузки")
	}
	info := e.info(it)
	old, had := e.index.Get(a.ID)

	if had && e.exists(old.Path, old.Size) && (old.LivePath == "" || e.exists(old.LivePath, old.LiveSize)) &&
		old.Checksum == sel.main.Checksum && (sel.live == nil) == (old.LivePath == "") &&
		(sel.live == nil || old.LiveChecksum == sel.live.Checksum) {
		if old.ChangeTag == a.ChangeTag {
			e.addBytes(old.Size + old.LiveSize)
			return true, nil
		}
		// файл тот же, поменялись только метаданные (альбомы, избранное, подпись…)
		side, err := e.applyMeta(old.Path, old.LivePath, info)
		if err != nil {
			return false, err
		}
		old.ChangeTag, old.Sidecar = a.ChangeTag, side
		e.recordSizes(&old)
		e.addBytes(old.Size + old.LiveSize)
		return true, e.index.Put(a.ID, old)
	}

	mainRel, liveRel := e.allocate(a, sel, old, had)
	if mainRel == "" {
		return false, errors.New("не удалось подобрать имя файла")
	}
	entry := state.Entry{Path: mainRel, LivePath: liveRel, ChangeTag: a.ChangeTag, Checksum: sel.main.Checksum}
	defer func() {
		if err != nil {
			e.index.Release(a.ID, mainRel, liveRel, mainRel+".xmp")
		}
	}()

	if entry.Size, err = e.fetch(ctx, &a, false, mainRel); err != nil {
		return false, err
	}
	if liveRel != "" {
		sel = selectResources(a) // ссылки могли обновиться
		entry.LiveChecksum = sel.live.Checksum
		if entry.LiveSize, err = e.fetch(ctx, &a, true, liveRel); err != nil {
			return false, err
		}
	}
	if entry.Sidecar, err = e.applyMeta(mainRel, liveRel, info); err != nil {
		return false, err
	}
	e.recordSizes(&entry)
	// прежняя версия этого же ассета под другим именем больше не нужна
	if had {
		for _, p := range []string{old.Path, old.LivePath, old.Sidecar} {
			if p != "" && p != entry.Path && p != entry.LivePath && p != entry.Sidecar {
				_ = os.Remove(e.abs(p))
			}
		}
	}
	return false, e.index.Put(a.ID, entry)
}

func (e *Engine) info(it *item) meta.Info {
	a := it.asset
	in := meta.Info{Caption: a.Caption, Favorite: a.Favorite, Keywords: it.keywords, Video: a.IsVideo()}
	if !a.Date.IsZero() {
		in.Date = a.LocalDate()
	}
	if a.Location != nil {
		in.Location = &meta.Location{Lat: a.Location.Lat, Lon: a.Location.Lon, Alt: a.Location.Alt}
	}
	return in
}

func (e *Engine) applyMeta(mainRel, liveRel string, info meta.Info) (string, error) {
	side, err := e.writer.Apply(e.abs(mainRel), info)
	if err != nil {
		return "", fmt.Errorf("метаданные: %w", err)
	}
	if liveRel != "" {
		li := info
		li.Video = true
		if _, err := e.writer.Apply(e.abs(liveRel), li); err != nil {
			return "", fmt.Errorf("метаданные видео: %w", err)
		}
	}
	if side == "" {
		return "", nil
	}
	rel, _ := filepath.Rel(e.opts.Dest, side)
	return filepath.ToSlash(rel), nil
}

func (e *Engine) abs(rel string) string { return filepath.Join(e.opts.Dest, filepath.FromSlash(rel)) }

// recordSizes запоминает размеры уже после записи метаданных: exiftool меняет файл,
// а при следующем запуске размер сверяется с тем, что лежит на диске.
func (e *Engine) recordSizes(en *state.Entry) {
	if fi, err := os.Stat(e.abs(en.Path)); err == nil {
		en.Size = fi.Size()
	}
	if en.LivePath != "" {
		if fi, err := os.Stat(e.abs(en.LivePath)); err == nil {
			en.LiveSize = fi.Size()
		}
	}
}

func (e *Engine) exists(rel string, size int64) bool {
	fi, err := os.Stat(e.abs(rel))
	return err == nil && fi.Mode().IsRegular() && fi.Size() == size
}

// allocate подбирает свободные пути; прежние пути ассета переиспользуются.
func (e *Engine) allocate(a icloud.Asset, sel selection, old state.Entry, had bool) (string, string) {
	next := candidates(a, e.opts.Layout, sel)
	if had {
		// при перекачке оставляем прежний каталог (раскладку не меняем задним числом)
		dir := filepath.ToSlash(filepath.Dir(old.Path))
		if dir == "." {
			dir = ""
		}
		layoutNext := next
		next = func(i int) (string, string) {
			m, l := layoutNext(i)
			m = joinRel(dir, filepath.Base(m))
			if l != "" {
				l = joinRel(dir, filepath.Base(l))
			}
			return m, l
		}
	}
	for i := 0; i < 100; i++ {
		m, l := next(i)
		if !e.free(a.ID, m) || l != "" && !e.free(a.ID, l) {
			continue
		}
		if e.index.Claim(a.ID, m, l, m+".xmp") {
			return m, l
		}
	}
	return "", ""
}

func joinRel(dir, name string) string {
	if dir == "" {
		return name
	}
	return dir + "/" + name
}

// free — путь не занят чужим ассетом и на диске нет постороннего файла.
func (e *Engine) free(id, rel string) bool {
	owner := e.index.Owner(rel)
	if owner != "" {
		return owner == id
	}
	_, err := os.Lstat(e.abs(rel))
	return errors.Is(err, fs.ErrNotExist)
}

// fetch скачивает основной (live=false) или видео-ресурс ассета в rel.
func (e *Engine) fetch(ctx context.Context, a *icloud.Asset, live bool, rel string) (int64, error) {
	dst := e.abs(rel)
	if err := os.MkdirAll(filepath.Dir(dst), 0o755); err != nil {
		return 0, err
	}
	tmp := dst + partSuffix
	defer os.Remove(tmp)

	refreshed := false
	var lastErr error
	for attempt := 0; attempt < 6; attempt++ {
		if attempt > 0 {
			select {
			case <-time.After(e.retryDelay(attempt - 1)):
			case <-ctx.Done():
				return 0, ctx.Err()
			}
		}
		sel := selectResources(*a)
		res := sel.main
		if live {
			res = sel.live
		}
		n, status, err := e.get(ctx, resourceURL(res.URL, rel), tmp)
		e.addBytes(n)
		switch {
		case err == nil && status == http.StatusOK:
			if res.Size > 0 && n != res.Size {
				e.addBytes(-n)
				lastErr = fmt.Errorf("размер %d вместо %d", n, res.Size)
				continue
			}
			if err := os.Rename(tmp, dst); err != nil {
				return 0, err
			}
			return n, nil
		case ctx.Err() != nil:
			return 0, ctx.Err()
		case err == nil && (status == 403 || status == 404 || status == 410) && !refreshed:
			// ссылка протухла — берём свежую
			e.addBytes(-n)
			refreshed = true
			fresh, rerr := e.src.Refresh(ctx, *a)
			if rerr != nil {
				return 0, rerr
			}
			*a = fresh
			attempt-- // обновление ссылки не считается попыткой
			continue
		case err == nil && status != 429 && status < 500:
			e.addBytes(-n)
			return 0, fmt.Errorf("HTTP %d", status)
		default:
			e.addBytes(-n)
			lastErr = err
			if err == nil {
				lastErr = fmt.Errorf("HTTP %d", status)
			}
		}
	}
	return 0, fmt.Errorf("не удалось скачать после повторов: %w", lastErr)
}

func resourceURL(u, rel string) string {
	return strings.ReplaceAll(u, "${f}", url.PathEscape(filepath.Base(rel)))
}

// get пишет тело ответа в tmp; при не-200 тело не сохраняется.
func (e *Engine) get(ctx context.Context, u, tmp string) (int64, int, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
	if err != nil {
		return 0, 0, err
	}
	resp, err := e.httpc.Do(req)
	if err != nil {
		return 0, 0, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 1<<16))
		return 0, resp.StatusCode, nil
	}
	f, err := os.Create(tmp)
	if err != nil {
		return 0, resp.StatusCode, err
	}
	n, err := io.Copy(f, &countingReader{r: resp.Body, add: e.addBytes})
	if cerr := f.Close(); err == nil {
		err = cerr
	}
	// countingReader уже учёл байты в прогрессе — fetch вычтет их при ошибке
	e.addBytes(-n)
	return n, resp.StatusCode, err
}

type countingReader struct {
	r   io.Reader
	add func(int64)
}

func (c *countingReader) Read(p []byte) (int, error) {
	n, err := c.r.Read(p)
	c.add(int64(n))
	return n, err
}
