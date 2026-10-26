package export

import (
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/rusjoan/icloud-export-app-macos/internal/icloud"
	"github.com/rusjoan/icloud-export-app-macos/internal/meta"
	"github.com/rusjoan/icloud-export-app-macos/internal/state"
)

type fakeSource struct {
	mu        sync.Mutex
	albums    map[string][]icloud.Asset
	refresh   func(icloud.Asset) icloud.Asset
	needLogin int // сколько раз вернуть ErrNeedLogin из Assets
	refreshed int
}

func (f *fakeSource) Assets(ctx context.Context, al icloud.Album, fn func(icloud.Asset) error) error {
	f.mu.Lock()
	if f.needLogin > 0 {
		f.needLogin--
		f.mu.Unlock()
		return icloud.ErrNeedLogin
	}
	list := f.albums[al.ID]
	f.mu.Unlock()
	for _, a := range list {
		if err := fn(a); err != nil {
			return err
		}
	}
	return nil
}

func (f *fakeSource) Refresh(ctx context.Context, a icloud.Asset) (icloud.Asset, error) {
	f.mu.Lock()
	f.refreshed++
	f.mu.Unlock()
	return f.refresh(a), nil
}

type fileServer struct {
	*httptest.Server
	hits  atomic.Int32
	mu    sync.Mutex
	files map[string]string
	fail  map[string][]int // путь → статусы, которые вернуть до успеха
}

func newFileServer(t *testing.T) *fileServer {
	fs := &fileServer{files: map[string]string{}, fail: map[string][]int{}}
	fs.Server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fs.hits.Add(1)
		fs.mu.Lock()
		defer fs.mu.Unlock()
		if st := fs.fail[r.URL.Path]; len(st) > 0 {
			fs.fail[r.URL.Path] = st[1:]
			w.WriteHeader(st[0])
			return
		}
		body, ok := fs.files[r.URL.Path]
		if !ok {
			w.WriteHeader(404)
			return
		}
		_, _ = w.Write([]byte(body))
	}))
	t.Cleanup(fs.Close)
	return fs
}

func (fs *fileServer) res(path, body, uti string) *icloud.Resource {
	fs.mu.Lock()
	fs.files[path] = body
	fs.mu.Unlock()
	return &icloud.Resource{URL: fs.URL + path, Size: int64(len(body)), FileType: uti, Checksum: "ck-" + path + "-" + strconv.Itoa(len(body))}
}

var june = time.Date(2024, 6, 10, 18, 30, 0, 0, time.UTC)

func run(t *testing.T, src Source, srv *fileServer, dest string, layout Layout, albums ...icloud.Album) Progress {
	t.Helper()
	e, err := Start(context.Background(), Options{Dest: dest, Layout: layout, Concurrency: 3, Albums: albums, RetryDelay: func(int) time.Duration { return 0 }}, src, meta.NewWriter(nil), srv.Client())
	if err != nil {
		t.Fatal(err)
	}
	select {
	case <-e.Done():
	case <-time.After(10 * time.Second):
		t.Fatal("export hung")
	}
	return e.Progress()
}

func readFile(t *testing.T, p string) string {
	t.Helper()
	b, err := os.ReadFile(p)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

func TestExportByDateThenResumeSkipsEverything(t *testing.T) {
	srv := newFileServer(t)
	dest := t.TempDir()
	live := icloud.Asset{ID: "A1", MasterID: "M1", Filename: "IMG_0001.HEIC", ItemType: "public.heic", Date: june, TZOffset: 3 * 3600,
		Original: srv.res("/1", "heic", "public.heic"), OrigLive: srv.res("/1v", "mov1", "com.apple.quicktime-movie"), ChangeTag: "t1"}
	edited := icloud.Asset{ID: "A2", MasterID: "M2", Filename: "IMG_0002.HEIC", ItemType: "public.heic", Date: june.AddDate(0, 1, 0), Hidden: true,
		Original: srv.res("/2", "orig2", "public.heic"), Adjusted: true, Edited: srv.res("/2e", "edited2", "public.jpeg"), ChangeTag: "t2"}
	video := icloud.Asset{ID: "A3", MasterID: "M3", Filename: "IMG_0003.MOV", ItemType: "com.apple.quicktime-movie",
		Original: srv.res("/3", "video3", "com.apple.quicktime-movie"), ChangeTag: "t3"}
	src := &fakeSource{albums: map[string][]icloud.Asset{"trip": {live, edited}, "hidden": {edited, video}}}
	albums := []icloud.Album{{ID: "trip", Name: "Поездка", Keyword: "Поездка"}, {ID: "hidden", Name: "Скрытые", Keyword: "Hidden"}}

	p := run(t, src, srv, dest, LayoutDate, albums...)
	if p.Phase != PhaseDone || p.Total != 3 || p.Downloaded != 3 || p.Failed != 0 {
		t.Fatalf("progress = %+v", p)
	}
	if got := readFile(t, filepath.Join(dest, "2024/06/IMG_0001.HEIC")); got != "heic" {
		t.Fatal("live photo still wrong")
	}
	if got := readFile(t, filepath.Join(dest, "2024/06/IMG_0001.MOV")); got != "mov1" {
		t.Fatal("live photo video wrong")
	}
	// правки вместо оригинала, расширение по типу рендера
	if got := readFile(t, filepath.Join(dest, "2024/07/IMG_0002.JPG")); got != "edited2" {
		t.Fatal("edited version expected")
	}
	if _, err := os.Stat(filepath.Join(dest, "2024/07/IMG_0002.HEIC")); err == nil {
		t.Fatal("original must not be saved when edited exists")
	}
	if _, err := os.Stat(filepath.Join(dest, "unknown/IMG_0003.MOV")); err != nil {
		t.Fatal("undated asset must go to unknown/")
	}
	// ассет из двух альбомов: оба ключевых слова, один файл
	x := readFile(t, filepath.Join(dest, "2024/07/IMG_0002.JPG.xmp"))
	if !strings.Contains(x, "<rdf:li>Поездка</rdf:li>") || strings.Count(x, "<rdf:li>Hidden</rdf:li>") != 1 {
		t.Fatalf("keywords in sidecar wrong:\n%s", x)
	}
	fi, _ := os.Stat(filepath.Join(dest, "2024/06/IMG_0001.HEIC"))
	if !fi.ModTime().Equal(june) {
		t.Fatalf("mtime = %v", fi.ModTime())
	}
	if p.Bytes != p.TotalBytes {
		t.Fatalf("bytes %d != total %d", p.Bytes, p.TotalBytes)
	}

	hits := srv.hits.Load()
	p = run(t, src, srv, dest, LayoutDate, albums...)
	if p.Skipped != 3 || p.Downloaded != 0 || srv.hits.Load() != hits {
		t.Fatalf("second run must skip: %+v, hits %d→%d", p, hits, srv.hits.Load())
	}
}

func TestCollisionsAndForeignFiles(t *testing.T) {
	srv := newFileServer(t)
	dest := t.TempDir()
	_ = os.WriteFile(filepath.Join(dest, "IMG_0001.JPG"), []byte("mine"), 0o644) // чужой файл
	a := icloud.Asset{ID: "AAA111zz", Filename: "IMG_0001.JPG", ItemType: "public.jpeg", Original: srv.res("/a", "a", "public.jpeg")}
	b := icloud.Asset{ID: "BBB222zz", Filename: "IMG_0001.JPG", ItemType: "public.jpeg", Original: srv.res("/b", "b", "public.jpeg")}
	src := &fakeSource{albums: map[string][]icloud.Asset{"x": {a, b}}}

	p := run(t, src, srv, dest, LayoutFlat, icloud.Album{ID: "x"})
	if p.Downloaded != 2 {
		t.Fatalf("progress = %+v", p)
	}
	if readFile(t, filepath.Join(dest, "IMG_0001.JPG")) != "mine" {
		t.Fatal("foreign file overwritten")
	}
	got := []string{readFile(t, filepath.Join(dest, "IMG_0001_AAA111.JPG")), readFile(t, filepath.Join(dest, "IMG_0001_BBB222.JPG"))}
	if !slices.Equal(got, []string{"a", "b"}) {
		t.Fatalf("collision names wrong: %v", got)
	}
	// стабильность имён между запусками
	p = run(t, src, srv, dest, LayoutFlat, icloud.Album{ID: "x"})
	if p.Skipped != 2 {
		t.Fatalf("rerun = %+v", p)
	}
}

func TestExpiredURLRefreshAndRetries(t *testing.T) {
	srv := newFileServer(t)
	dest := t.TempDir()
	a := icloud.Asset{ID: "A", Filename: "x.JPG", ItemType: "public.jpeg", Original: srv.res("/old", "zzz", "public.jpeg")}
	srv.fail["/old"] = []int{410}
	srv.fail["/new"] = []int{503, 500}
	src := &fakeSource{albums: map[string][]icloud.Asset{"x": {a}}, refresh: func(a icloud.Asset) icloud.Asset {
		a.Original = srv.res("/new", "fresh", "public.jpeg")
		return a
	}}
	p := run(t, src, srv, dest, LayoutFlat, icloud.Album{ID: "x"})
	if p.Downloaded != 1 || src.refreshed != 1 || readFile(t, filepath.Join(dest, "x.JPG")) != "fresh" {
		t.Fatalf("progress = %+v refreshed=%d", p, src.refreshed)
	}
}

func TestPermanentFailureIsRecordedAndNotFatal(t *testing.T) {
	srv := newFileServer(t)
	dest := t.TempDir()
	bad := icloud.Asset{ID: "BAD", Filename: "bad.JPG", ItemType: "public.jpeg", Original: &icloud.Resource{URL: srv.URL + "/missing", FileType: "public.jpeg"}}
	good := icloud.Asset{ID: "OK", Filename: "good.JPG", ItemType: "public.jpeg", Original: srv.res("/g", "g", "public.jpeg")}
	src := &fakeSource{albums: map[string][]icloud.Asset{"x": {bad, good}}, refresh: func(a icloud.Asset) icloud.Asset { return a }}
	p := run(t, src, srv, dest, LayoutFlat, icloud.Album{ID: "x"})
	if p.Phase != PhaseDone || p.Failed != 1 || p.Downloaded != 1 || len(p.Errors) != 1 {
		t.Fatalf("progress = %+v", p)
	}
	if !strings.Contains(readFile(t, filepath.Join(dest, errLogName)), "BAD") {
		t.Fatal("error not logged")
	}
	if _, err := os.Stat(filepath.Join(dest, "bad.JPG")); err == nil {
		t.Fatal("failed file must not exist")
	}
	// имя не должно остаться занятым за неудачным ассетом
	ix, _ := state.Open(dest)
	if ix.Owner("bad.JPG") != "" {
		t.Fatal("failed asset kept its path claim")
	}
}

func TestChangedFileRedownloadsAndRemovesOldVersion(t *testing.T) {
	srv := newFileServer(t)
	dest := t.TempDir()
	a := icloud.Asset{ID: "A", Filename: "IMG_5.HEIC", ItemType: "public.heic", Original: srv.res("/o", "orig", "public.heic"), ChangeTag: "1"}
	src := &fakeSource{albums: map[string][]icloud.Asset{"x": {a}}}
	run(t, src, srv, dest, LayoutFlat, icloud.Album{ID: "x"})

	// отредактировали в «Фото» → теперь качается JPEG-рендер
	a.Adjusted, a.Edited, a.ChangeTag = true, srv.res("/e", "edit", "public.jpeg"), "2"
	src.albums["x"] = []icloud.Asset{a}
	p := run(t, src, srv, dest, LayoutFlat, icloud.Album{ID: "x"})
	if p.Downloaded != 1 || readFile(t, filepath.Join(dest, "IMG_5.JPG")) != "edit" {
		t.Fatalf("progress = %+v", p)
	}
	if _, err := os.Stat(filepath.Join(dest, "IMG_5.HEIC")); err == nil {
		t.Fatal("stale version of the same asset must be removed")
	}

	// поменялось только избранное → без загрузки, метаданные обновлены
	hits := srv.hits.Load()
	a.Favorite, a.ChangeTag = true, "3"
	src.albums["x"] = []icloud.Asset{a}
	p = run(t, src, srv, dest, LayoutFlat, icloud.Album{ID: "x"})
	if p.Skipped != 1 || srv.hits.Load() != hits || !strings.Contains(readFile(t, filepath.Join(dest, "IMG_5.JPG.xmp")), `xmp:Rating="5"`) {
		t.Fatalf("metadata-only change: %+v", p)
	}
}

func TestStalePartsCleanedOnlyOurs(t *testing.T) {
	srv := newFileServer(t)
	dest := t.TempDir()
	_ = os.WriteFile(filepath.Join(dest, "x.JPG"+partSuffix), []byte("half"), 0o644)
	_ = os.WriteFile(filepath.Join(dest, "movie.part"), []byte("user"), 0o644)
	run(t, &fakeSource{}, srv, dest, LayoutFlat)
	if _, err := os.Stat(filepath.Join(dest, "x.JPG"+partSuffix)); err == nil {
		t.Fatal("our stale part not removed")
	}
	if _, err := os.Stat(filepath.Join(dest, "movie.part")); err != nil {
		t.Fatal("foreign .part removed")
	}
}

func TestNeedLoginPausesUntilResume(t *testing.T) {
	srv := newFileServer(t)
	dest := t.TempDir()
	a := icloud.Asset{ID: "A", Filename: "a.JPG", ItemType: "public.jpeg", Original: srv.res("/a", "a", "public.jpeg")}
	src := &fakeSource{albums: map[string][]icloud.Asset{"x": {a}}, needLogin: 1}
	e, err := Start(context.Background(), Options{Dest: dest, Layout: LayoutFlat, Albums: []icloud.Album{{ID: "x"}}}, src, meta.NewWriter(nil), srv.Client())
	if err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(5 * time.Second)
	for e.Progress().Phase != PhaseNeedLogin {
		if time.Now().After(deadline) {
			t.Fatalf("phase = %s", e.Progress().Phase)
		}
		time.Sleep(10 * time.Millisecond)
	}
	e.Resume()
	<-e.Done()
	if p := e.Progress(); p.Phase != PhaseDone || p.Downloaded != 1 {
		t.Fatalf("progress = %+v", p)
	}
}

func TestCancelStops(t *testing.T) {
	srv := newFileServer(t)
	dest := t.TempDir()
	var list []icloud.Asset
	for i := range 50 {
		id := string(rune('a'+i%26)) + strings.Repeat("x", i/26+1)
		list = append(list, icloud.Asset{ID: id, Filename: id + ".JPG", ItemType: "public.jpeg", Original: srv.res("/"+id, id, "public.jpeg")})
	}
	src := &fakeSource{albums: map[string][]icloud.Asset{"x": list}}
	e, _ := Start(context.Background(), Options{Dest: dest, Concurrency: 1, Albums: []icloud.Album{{ID: "x"}}}, src, meta.NewWriter(nil), srv.Client())
	e.Pause()
	e.Cancel()
	<-e.Done()
	if p := e.Progress(); p.Phase != PhaseCancelled {
		t.Fatalf("phase = %s", p.Phase)
	}
	if _, err := os.Stat(filepath.Join(dest, state.FileName)); err != nil {
		t.Fatal("index must be saved on cancel")
	}
}

func TestResumeAfterMetadataChangesFileSize(t *testing.T) {
	srv := newFileServer(t)
	dest := t.TempDir()
	a := icloud.Asset{ID: "A", Filename: "a.JPG", ItemType: "public.jpeg", Date: june, Original: srv.res("/a", "abc", "public.jpeg"), ChangeTag: "1"}
	src := &fakeSource{albums: map[string][]icloud.Asset{"x": {a}}}
	run(t, src, srv, dest, LayoutFlat, icloud.Album{ID: "x"})

	// как будто exiftool записал метаданные внутрь файла
	p := filepath.Join(dest, "a.JPG")
	_ = os.WriteFile(p, []byte("abc+exif-metadata"), 0o644)
	ix, _ := state.Open(dest)
	en, _ := ix.Get("A")
	en.Size = int64(len("abc+exif-metadata"))
	_ = ix.Put("A", en)
	_ = ix.Save()

	hits := srv.hits.Load()
	pr := run(t, src, srv, dest, LayoutFlat, icloud.Album{ID: "x"})
	if pr.Skipped != 1 || srv.hits.Load() != hits {
		t.Fatalf("file with embedded metadata re-downloaded: %+v", pr)
	}
}

func TestRecordSizesUsesSizeOnDisk(t *testing.T) {
	dest := t.TempDir()
	_ = os.WriteFile(filepath.Join(dest, "a.JPG"), []byte("0123456789"), 0o644)
	_ = os.WriteFile(filepath.Join(dest, "a.MOV"), []byte("01234"), 0o644)
	e := &Engine{opts: Options{Dest: dest}}
	en := state.Entry{Path: "a.JPG", LivePath: "a.MOV", Size: 3, LiveSize: 1}
	e.recordSizes(&en)
	if en.Size != 10 || en.LiveSize != 5 {
		t.Fatalf("sizes = %d/%d", en.Size, en.LiveSize)
	}
}
