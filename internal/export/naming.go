package export

import (
	"path"
	"path/filepath"
	"strconv"
	"strings"
	"unicode"

	"github.com/rusjoan/icloud-export-app-macos/internal/icloud"
)

type Layout string

const (
	LayoutFlat Layout = "flat" // всё в одну папку
	LayoutDate Layout = "date" // ГГГГ/ММ/
)

// selection — что скачиваем для ассета: правки вместо оригинала, если они есть.
type selection struct {
	main, live *icloud.Resource
}

func selectResources(a icloud.Asset) selection {
	s := selection{main: a.Original, live: a.OrigLive}
	if a.Adjusted && a.Edited != nil {
		s.main = a.Edited
		if a.EditLive != nil {
			s.live = a.EditLive
		}
	}
	return s
}

// dirFor — каталог ассета относительно корня выгрузки.
func dirFor(a icloud.Asset, layout Layout) string {
	if layout != LayoutDate {
		return ""
	}
	if a.Date.IsZero() {
		return "unknown"
	}
	return a.LocalDate().Format("2006/01")
}

// baseName — имя файла без расширения: «2024-06-10 21.30.05 IMG_1234».
// Дата съёмки (в поясе снимка) впереди — по имени файлы идут хронологически,
// исходное имя сохраняется, поэтому совпадения почти исключены.
func baseName(a icloud.Asset) string {
	name := strings.TrimSuffix(a.Filename, filepath.Ext(a.Filename))
	name = sanitize(name)
	if name == "" {
		name = "IMG_" + shortID(a.ID)
	}
	if !a.Date.IsZero() {
		name = a.LocalDate().Format("2006-01-02 15.04.05") + " " + name
	}
	return name
}

// extFor — расширение по типу ресурса, иначе из исходного имени.
func extFor(r *icloud.Resource, a icloud.Asset, video bool) string {
	if r != nil {
		if e := icloud.ExtForType(r.FileType); e != "" {
			return e
		}
	}
	if video {
		return ".MOV"
	}
	if e := filepath.Ext(a.Filename); e != "" && len(e) <= 6 {
		return sanitize(e)
	}
	return ".bin"
}

func sanitize(s string) string {
	s = strings.Map(func(r rune) rune {
		switch {
		case r == '/' || r == ':' || r == '\\' || unicode.IsControl(r):
			return '_'
		}
		return r
	}, s)
	return strings.Trim(strings.TrimSpace(s), ".")
}

func shortID(id string) string {
	var b strings.Builder
	for _, r := range id {
		if r < 128 && (unicode.IsLetter(r) || unicode.IsDigit(r)) {
			b.WriteRune(r)
			if b.Len() == 6 {
				break
			}
		}
	}
	if b.Len() == 0 {
		return "x"
	}
	return b.String()
}

// candidates — варианты путей (main, live) в порядке предпочтения.
func candidates(a icloud.Asset, layout Layout, sel selection) func(i int) (string, string) {
	dir, base := dirFor(a, layout), baseName(a)
	mainExt := extFor(sel.main, a, a.IsVideo())
	liveExt := ""
	if sel.live != nil {
		liveExt = extFor(sel.live, a, true)
		if strings.EqualFold(liveExt, mainExt) {
			liveExt = "_live" + liveExt
		}
	}
	return func(i int) (string, string) {
		b := base
		if i > 0 {
			b += "_" + strconv.Itoa(i+1)
		}
		m := path.Join(dir, b+mainExt)
		if liveExt == "" {
			return m, ""
		}
		return m, path.Join(dir, b+liveExt)
	}
}
