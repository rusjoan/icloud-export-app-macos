// Package state — индекс выгрузки в корне папки назначения: что скачано и куда.
package state

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync"
)

const FileName = ".icloud-export.json"

type Entry struct {
	Path      string `json:"path"`               // относительно корня выгрузки
	LivePath  string `json:"livePath,omitempty"` // видео Live Photo
	Sidecar   string `json:"sidecar,omitempty"`
	Size      int64  `json:"size"`
	LiveSize  int64  `json:"liveSize,omitempty"`
	ChangeTag string `json:"changeTag"`
	// контрольные суммы скачанных ресурсов: совпали — перекачивать не нужно
	Checksum     string `json:"checksum,omitempty"`
	LiveChecksum string `json:"liveChecksum,omitempty"`
}

type file struct {
	Version int              `json:"version"`
	Layout  string           `json:"layout"`
	Assets  map[string]Entry `json:"assets"`
}

// Index — потокобезопасный индекс, сохраняется атомарно.
type Index struct {
	mu    sync.Mutex
	root  string
	data  file
	paths map[string]string // путь (в нижнем регистре) → assetID
	dirty int
}

// Open читает индекс из root (или создаёт пустой).
func Open(root string) (*Index, error) {
	ix := &Index{root: root, data: file{Version: 1, Assets: map[string]Entry{}}}
	raw, err := os.ReadFile(filepath.Join(root, FileName))
	switch {
	case errors.Is(err, os.ErrNotExist):
	case err != nil:
		return nil, err
	default:
		if err := json.Unmarshal(raw, &ix.data); err != nil {
			return nil, errors.New("индекс выгрузки повреждён: " + err.Error())
		}
		if ix.data.Assets == nil {
			ix.data.Assets = map[string]Entry{}
		}
	}
	ix.rebuildPaths()
	return ix, nil
}

func (ix *Index) rebuildPaths() {
	ix.paths = map[string]string{}
	for id, e := range ix.data.Assets {
		for _, p := range []string{e.Path, e.LivePath, e.Sidecar} {
			if p != "" {
				ix.paths[key(p)] = id
			}
		}
	}
}

// key — macOS по умолчанию нечувствительна к регистру имён.
func key(p string) string { return strings.ToLower(filepath.ToSlash(p)) }

// Layout — раскладка, с которой индекс создан ("" — новый индекс).
func (ix *Index) Layout() string {
	ix.mu.Lock()
	defer ix.mu.Unlock()
	return ix.data.Layout
}

func (ix *Index) SetLayout(l string) {
	ix.mu.Lock()
	ix.data.Layout = l
	ix.mu.Unlock()
}

func (ix *Index) Len() int {
	ix.mu.Lock()
	defer ix.mu.Unlock()
	return len(ix.data.Assets)
}

func (ix *Index) Get(id string) (Entry, bool) {
	ix.mu.Lock()
	defer ix.mu.Unlock()
	e, ok := ix.data.Assets[id]
	return e, ok
}

// Owner — какому ассету принадлежит путь ("" — никому).
func (ix *Index) Owner(rel string) string {
	ix.mu.Lock()
	defer ix.mu.Unlock()
	return ix.paths[key(rel)]
}

// Claim резервирует пути за ассетом; false — какой-то путь занят другим ассетом.
func (ix *Index) Claim(id string, rels ...string) bool {
	ix.mu.Lock()
	defer ix.mu.Unlock()
	for _, r := range rels {
		if r == "" {
			continue
		}
		if o, ok := ix.paths[key(r)]; ok && o != id {
			return false
		}
	}
	for _, r := range rels {
		if r != "" {
			ix.paths[key(r)] = id
		}
	}
	return true
}

// Release снимает резерв путей (если загрузка не удалась).
func (ix *Index) Release(id string, rels ...string) {
	ix.mu.Lock()
	defer ix.mu.Unlock()
	e, has := ix.data.Assets[id]
	for _, r := range rels {
		if r == "" || has && (r == e.Path || r == e.LivePath || r == e.Sidecar) {
			continue
		}
		if ix.paths[key(r)] == id {
			delete(ix.paths, key(r))
		}
	}
}

// Put записывает результат загрузки и раз в 50 записей сохраняет индекс.
func (ix *Index) Put(id string, e Entry) error {
	ix.mu.Lock()
	if old, ok := ix.data.Assets[id]; ok {
		for _, p := range []string{old.Path, old.LivePath, old.Sidecar} {
			if p != "" && ix.paths[key(p)] == id {
				delete(ix.paths, key(p))
			}
		}
	}
	ix.data.Assets[id] = e
	for _, p := range []string{e.Path, e.LivePath, e.Sidecar} {
		if p != "" {
			ix.paths[key(p)] = id
		}
	}
	ix.dirty++
	flush := ix.dirty >= 50
	ix.mu.Unlock()
	if flush {
		return ix.Save()
	}
	return nil
}

// Save атомарно пишет индекс на диск.
func (ix *Index) Save() error {
	ix.mu.Lock()
	raw, err := json.MarshalIndent(ix.data, "", " ")
	ix.dirty = 0
	ix.mu.Unlock()
	if err != nil {
		return err
	}
	tmp := filepath.Join(ix.root, FileName+".tmp")
	if err := os.WriteFile(tmp, raw, 0o644); err != nil {
		return err
	}
	return os.Rename(tmp, filepath.Join(ix.root, FileName))
}
