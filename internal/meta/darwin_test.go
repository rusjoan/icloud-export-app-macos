//go:build darwin

package meta

import (
	"encoding/json"
	"image"
	"image/jpeg"
	"os"
	"os/exec"
	"path/filepath"
	"syscall"
	"testing"
	"time"
)

func TestDarwinBirthtime(t *testing.T) {
	p := filepath.Join(t.TempDir(), "f")
	_ = os.WriteFile(p, []byte("x"), 0o644)
	want := time.Date(2009, 2, 13, 23, 31, 30, 0, time.UTC)
	if err := SetTimes(p, want); err != nil {
		t.Fatal(err)
	}
	var st syscall.Stat_t
	if err := syscall.Stat(p, &st); err != nil {
		t.Fatal(err)
	}
	if got := time.Unix(st.Birthtimespec.Unix()); !got.Equal(want) {
		t.Fatalf("birthtime = %v, want %v", got, want)
	}
	if got := time.Unix(st.Mtimespec.Unix()); !got.Equal(want) {
		t.Fatalf("mtime = %v, want %v", got, want)
	}
}

func TestDarwinExifToolWritesJPEG(t *testing.T) {
	bin := FindTool("exiftool")
	if bin == "" {
		t.Skip("exiftool не установлен")
	}
	dir := t.TempDir()
	p := filepath.Join(dir, "IMG_1.JPG")
	f, _ := os.Create(p)
	_ = jpeg.Encode(f, image.NewRGBA(image.Rect(0, 0, 8, 8)), nil)
	f.Close()

	et, err := StartExifTool(bin)
	if err != nil {
		t.Fatal(err)
	}
	defer et.Close()
	info := sample()
	side, err := NewWriter(et).Apply(p, info)
	if err != nil || side != "" {
		t.Fatalf("Apply: side=%q err=%v", side, err)
	}

	out, err := exec.Command(bin, "-j", "-n", "-DateTimeOriginal", "-OffsetTimeOriginal", "-GPSLatitude",
		"-GPSLongitude", "-Caption-Abstract", "-Description", "-Rating", "-Subject", p).Output()
	if err != nil {
		t.Fatal(err)
	}
	var got []map[string]any
	_ = json.Unmarshal(out, &got)
	g := got[0]
	if g["DateTimeOriginal"] != "2024:06:10 21:30:05" || g["OffsetTimeOriginal"] != "+03:00" ||
		g["GPSLatitude"].(float64) != -33.856 || g["Rating"].(float64) != 5 ||
		g["Description"] != "Опера & <гавань>\nвторая строка" {
		t.Fatalf("written tags: %v", g)
	}
	if subj, _ := g["Subject"].([]any); len(subj) != 2 {
		t.Fatalf("subject = %v", g["Subject"])
	}
	var st syscall.Stat_t
	_ = syscall.Stat(p, &st)
	if !time.Unix(st.Birthtimespec.Unix()).Equal(info.Date) {
		t.Fatal("birthtime not set after exiftool")
	}
}
