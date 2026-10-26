package meta

import (
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"
)

var msk = time.FixedZone("", 3*3600)

func sample() Info {
	alt := -12.5
	return Info{
		Date:     time.Date(2024, 6, 10, 21, 30, 5, 0, msk),
		Location: &Location{Lat: -33.856, Lon: 151.215, Alt: &alt},
		Caption:  "Опера & <гавань>\nвторая строка",
		Favorite: true,
		Keywords: []string{"Отпуск 2024", "Hidden"},
	}
}

func TestExifArgsPhotoJPEG(t *testing.T) {
	args := ExifArgs("/x/IMG_1.JPG", sample())
	for _, want := range []string{
		"-EXIF:DateTimeOriginal=2024:06:10 21:30:05", "-EXIF:OffsetTimeOriginal=+03:00",
		"-XMP-photoshop:DateCreated=2024:06:10 21:30:05+03:00",
		"-EXIF:GPSLatitude=33.856000", "-EXIF:GPSLatitudeRef=S", "-EXIF:GPSLongitudeRef=E",
		"-EXIF:GPSAltitude=12.5", "-EXIF:GPSAltitudeRef=1",
		"-IPTC:Caption-Abstract=Опера & <гавань>\nвторая строка", "-IPTC:CodedCharacterSet=UTF8",
		"-XMP-xmp:Rating=5", "-XMP-dc:Subject=Отпуск 2024", "-IPTC:Keywords=Hidden",
	} {
		if !slices.Contains(args, want) {
			t.Errorf("missing %q", want)
		}
	}
	if args[len(args)-1] != "/x/IMG_1.JPG" {
		t.Fatal("path must be last")
	}
}

func TestExifArgsHEICHasNoIPTCAndVideoUsesKeys(t *testing.T) {
	for _, a := range ExifArgs("/x/IMG_1.HEIC", sample()) {
		if strings.HasPrefix(a, "-IPTC:") {
			t.Fatalf("IPTC for HEIC: %s", a)
		}
	}
	v := sample()
	v.Video = true
	args := ExifArgs("/x/IMG_1.MOV", v)
	for _, want := range []string{"QuickTimeUTC=1", "-QuickTime:CreateDate=2024:06:10 21:30:05+03:00",
		"-Keys:CreationDate=2024:06:10 21:30:05+03:00", "-Keys:GPSCoordinates=-33.856000, 151.215000, -12.5"} {
		if !slices.Contains(args, want) {
			t.Errorf("missing %q in %v", want, args)
		}
	}
}

func TestExifArgsOmitsUnknownFields(t *testing.T) {
	args := ExifArgs("/x/a.jpg", Info{})
	if len(args) != 3 { // -overwrite_original -m path
		t.Fatalf("empty info must not write tags: %v", args)
	}
}

func TestArgLineEscapesNewlines(t *testing.T) {
	if got := argLine("-Caption=a\\b\nc"); got != `#[CSTR]-Caption=a\\b\nc` {
		t.Fatalf("argLine = %q", got)
	}
	if got := argLine("-Caption=plain"); got != "-Caption=plain" {
		t.Fatalf("argLine = %q", got)
	}
}

func TestXMPSidecar(t *testing.T) {
	x := XMP(sample())
	for _, want := range []string{
		`xmp:CreateDate="2024-06-10T21:30:05+03:00"`, `exif:GPSLatitude="33,51.360000S"`,
		`exif:GPSAltitude="125/10"`, `exif:GPSAltitudeRef="1"`, `xmp:Rating="5"`,
		"Опера &amp; &lt;гавань&gt;", "<rdf:li>Отпуск 2024</rdf:li>",
	} {
		if !strings.Contains(x, want) {
			t.Errorf("XMP missing %q", want)
		}
	}
}

func TestApplyWithoutExifToolWritesSidecarAndTimes(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, "IMG_1.HEIC")
	_ = os.WriteFile(p, []byte("data"), 0o644)
	w := NewWriter(nil)
	info := sample()
	side, err := w.Apply(p, info)
	if err != nil {
		t.Fatal(err)
	}
	if side != p+".xmp" {
		t.Fatalf("sidecar = %q", side)
	}
	for _, f := range []string{p, side} {
		fi, _ := os.Stat(f)
		if !fi.ModTime().Equal(info.Date) {
			t.Fatalf("%s mtime = %v", f, fi.ModTime())
		}
	}
	if b, _ := os.ReadFile(p); string(b) != "data" {
		t.Fatal("original bytes changed")
	}
}

func TestApplyEmptyInfoNoSidecar(t *testing.T) {
	p := filepath.Join(t.TempDir(), "a.jpg")
	_ = os.WriteFile(p, nil, 0o644)
	if side, err := NewWriter(nil).Apply(p, Info{}); err != nil || side != "" {
		t.Fatalf("side=%q err=%v", side, err)
	}
}
