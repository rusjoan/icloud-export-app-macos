// Package meta записывает метаданные из iCloud в файлы и выставляет даты файлов.
package meta

import (
	"fmt"
	"math"
	"os"
	"path/filepath"
	"strings"
	"time"
)

type Location struct {
	Lat, Lon float64
	Alt      *float64
}

// Info — метаданные ассета, которые нужно сохранить.
type Info struct {
	Date     time.Time // дата съёмки в поясе снимка; нулевая — неизвестна
	Location *Location
	Caption  string
	Favorite bool
	Keywords []string
	Video    bool
}

// Writer пишет метаданные внутрь файла через exiftool, иначе — в XMP-сайдкар.
type Writer struct {
	et *ExifTool // nil — exiftool нет
}

func NewWriter(et *ExifTool) *Writer { return &Writer{et: et} }

// HasExifTool — пишет ли Writer внутрь файлов.
func (w *Writer) HasExifTool() bool { return w.et != nil }

// Apply записывает метаданные и выставляет даты. Возвращает путь сайдкара,
// если он понадобился (exiftool нет или не смог записать файл).
func (w *Writer) Apply(path string, info Info) (sidecar string, err error) {
	if w.et != nil {
		if err := w.et.Write(path, ExifArgs(path, info)); err == nil {
			return "", SetTimes(path, info.Date)
		}
	}
	if info.empty() {
		return "", SetTimes(path, info.Date)
	}
	sidecar = path + ".xmp"
	if err := os.WriteFile(sidecar, []byte(XMP(info)), 0o644); err != nil {
		return "", err
	}
	if err := SetTimes(sidecar, info.Date); err != nil {
		return sidecar, err
	}
	return sidecar, SetTimes(path, info.Date)
}

func (i Info) empty() bool {
	return i.Date.IsZero() && i.Location == nil && i.Caption == "" && !i.Favorite && len(i.Keywords) == 0
}

// SetTimes ставит дату изменения и дату создания файла (нулевая дата — ничего не делает).
func SetTimes(path string, t time.Time) error {
	if t.IsZero() {
		return nil
	}
	if err := os.Chtimes(path, t, t); err != nil {
		return err
	}
	return setBirthtime(path, t)
}

const (
	exifDate = "2006:01:02 15:04:05"
	zoneFmt  = "-07:00"
)

// ExifArgs — аргументы exiftool для записи Info в файл path.
func ExifArgs(path string, info Info) []string {
	ext := strings.ToLower(filepath.Ext(path))
	iptc := ext == ".jpg" || ext == ".jpeg" || ext == ".tif" || ext == ".tiff"
	args := []string{"-overwrite_original", "-m"}
	add := func(tag, val string) { args = append(args, "-"+tag+"="+val) }

	if info.Video {
		args = append(args, "-api", "QuickTimeUTC=1")
		if !info.Date.IsZero() {
			d := info.Date.Format(exifDate + zoneFmt)
			add("QuickTime:CreateDate", d)
			add("Keys:CreationDate", d)
		}
		if l := info.Location; l != nil {
			v := fmt.Sprintf("%.6f, %.6f", l.Lat, l.Lon)
			if l.Alt != nil {
				v += fmt.Sprintf(", %.1f", *l.Alt)
			}
			add("Keys:GPSCoordinates", v)
		}
		if info.Caption != "" {
			add("Keys:Description", info.Caption)
			add("XMP-dc:Description", info.Caption)
		}
	} else {
		if !info.Date.IsZero() {
			d, z := info.Date.Format(exifDate), info.Date.Format(zoneFmt)
			add("EXIF:DateTimeOriginal", d)
			add("EXIF:CreateDate", d)
			add("EXIF:OffsetTimeOriginal", z)
			add("EXIF:OffsetTimeDigitized", z)
			add("XMP-photoshop:DateCreated", d+z)
			add("XMP-xmp:CreateDate", d+z)
		}
		if l := info.Location; l != nil {
			add("EXIF:GPSLatitude", fmt.Sprintf("%.6f", math.Abs(l.Lat)))
			add("EXIF:GPSLatitudeRef", map[bool]string{true: "N", false: "S"}[l.Lat >= 0])
			add("EXIF:GPSLongitude", fmt.Sprintf("%.6f", math.Abs(l.Lon)))
			add("EXIF:GPSLongitudeRef", map[bool]string{true: "E", false: "W"}[l.Lon >= 0])
			if l.Alt != nil {
				add("EXIF:GPSAltitude", fmt.Sprintf("%.1f", math.Abs(*l.Alt)))
				add("EXIF:GPSAltitudeRef", map[bool]string{true: "0", false: "1"}[*l.Alt >= 0])
			}
		}
		if info.Caption != "" {
			add("XMP-dc:Description", info.Caption)
			add("EXIF:ImageDescription", info.Caption)
			if iptc {
				add("IPTC:Caption-Abstract", info.Caption)
			}
		}
		if iptc && (info.Caption != "" || len(info.Keywords) > 0) {
			add("IPTC:CodedCharacterSet", "UTF8")
		}
	}
	if info.Favorite {
		add("XMP-xmp:Rating", "5")
	}
	for _, k := range info.Keywords {
		add("XMP-dc:Subject", k)
		if iptc && !info.Video {
			add("IPTC:Keywords", k)
		}
	}
	return append(args, path)
}
