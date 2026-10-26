//go:build darwin

package export

import (
	"bytes"
	"context"
	"image"
	"image/jpeg"
	"os"
	"path/filepath"
	"syscall"
	"testing"
	"time"

	"github.com/rusjoan/icloud-export-app-macos/internal/icloud"
	"github.com/rusjoan/icloud-export-app-macos/internal/meta"
)

// Реальный exiftool меняет файл; повторный запуск всё равно ничего не качает.
func TestDarwinRealExifToolResume(t *testing.T) {
	bin := meta.FindTool("exiftool")
	if bin == "" {
		t.Skip("exiftool не установлен")
	}
	var buf bytes.Buffer
	_ = jpeg.Encode(&buf, image.NewRGBA(image.Rect(0, 0, 8, 8)), nil)
	srv := newFileServer(t)
	dest := t.TempDir()
	a := icloud.Asset{ID: "A", Filename: "IMG_1.JPG", ItemType: "public.jpeg", Date: june, TZOffset: 3 * 3600, Favorite: true,
		Original: srv.res("/a", buf.String(), "public.jpeg"), ChangeTag: "1"}
	src := &fakeSource{albums: map[string][]icloud.Asset{"x": {a}}}

	runWith := func() Progress {
		et, err := meta.StartExifTool(bin)
		if err != nil {
			t.Fatal(err)
		}
		defer et.Close()
		e, err := Start(context.Background(), Options{Dest: dest, Layout: LayoutDate, Albums: []icloud.Album{{ID: "x", Keyword: "Trip"}}},
			src, meta.NewWriter(et), srv.Client())
		if err != nil {
			t.Fatal(err)
		}
		<-e.Done()
		return e.Progress()
	}
	if p := runWith(); p.Downloaded != 1 || p.Failed != 0 {
		t.Fatalf("first run: %+v", p)
	}
	p := filepath.Join(dest, "2024/06/2024-06-10 21.30.00 IMG_1.JPG")
	if _, err := os.Stat(p + ".xmp"); err == nil {
		t.Fatal("sidecar must not be written when exiftool succeeded")
	}
	var st syscall.Stat_t
	_ = syscall.Stat(p, &st)
	if !time.Unix(st.Birthtimespec.Unix()).Equal(june) {
		t.Fatalf("birthtime = %v", time.Unix(st.Birthtimespec.Unix()))
	}
	hits := srv.hits.Load()
	if p := runWith(); p.Skipped != 1 || srv.hits.Load() != hits {
		t.Fatalf("second run re-downloaded: %+v", p)
	}
}
