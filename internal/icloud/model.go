package icloud

import (
	"encoding/base64"
	"encoding/json"
	"strings"
	"time"
	"unicode/utf8"

	"howett.net/plist"
)

// AlbumKind — тип альбома.
type AlbumKind string

const (
	KindSmart   AlbumKind = "smart"
	KindUser    AlbumKind = "user"
	KindFolder  AlbumKind = "folder"
	KindHidden  AlbumKind = "hidden"
	KindDeleted AlbumKind = "deleted"
)

type Album struct {
	ID       string    `json:"id"`
	Name     string    `json:"name"`
	Kind     AlbumKind `json:"kind"`
	ParentID string    `json:"parentId,omitempty"`
	Count    int       `json:"count"`
	// Keyword — что писать в ключевые слова файла (пусто — не писать).
	Keyword string `json:"-"`

	listType string
	objType  string
	filters  []map[string]any
}

// Resource — один скачиваемый файл записи.
type Resource struct {
	URL      string `json:"-"`
	Size     int64  `json:"size"`
	FileType string `json:"fileType"`
	Checksum string `json:"checksum"`
}

type Location struct {
	Lat, Lon float64
	Alt      *float64
}

// Asset — фото или видео вместе с данными мастер-записи.
type Asset struct {
	ID        string
	MasterID  string
	Filename  string
	ItemType  string
	Date      time.Time // дата съёмки (с учётом правок в «Фото»), UTC
	TZOffset  int       // смещение пояса снимка, секунды
	Favorite  bool
	Hidden    bool
	Deleted   bool
	Caption   string
	Location  *Location
	Adjusted  bool
	Original  *Resource
	OrigLive  *Resource // видео Live Photo
	Edited    *Resource // рендер правок (фото или видео)
	EditLive  *Resource // рендер правок видео Live Photo
	ChangeTag string    // меняется при любом изменении записи
}

func (a *Asset) IsVideo() bool { return isVideoType(a.ItemType) }

// LocalDate — дата съёмки в часовом поясе снимка.
func (a *Asset) LocalDate() time.Time {
	return a.Date.In(time.FixedZone("", a.TZOffset))
}

type ckRecord struct {
	RecordName      string                     `json:"recordName"`
	RecordType      string                     `json:"recordType"`
	RecordChangeTag string                     `json:"recordChangeTag"`
	Fields          map[string]json.RawMessage `json:"fields"`
	ServerErrorCode string                     `json:"serverErrorCode"`
}

type ckField struct {
	Value json.RawMessage `json:"value"`
	Type  string          `json:"type"`
}

func (r *ckRecord) field(name string) json.RawMessage {
	raw, ok := r.Fields[name]
	if !ok {
		return nil
	}
	var f ckField
	if json.Unmarshal(raw, &f) != nil {
		return nil
	}
	return f.Value
}

func (r *ckRecord) str(name string) string {
	var s string
	_ = json.Unmarshal(r.field(name), &s)
	return s
}

func (r *ckRecord) num(name string) int64 {
	var n json.Number
	if json.Unmarshal(r.field(name), &n) != nil {
		return 0
	}
	v, _ := n.Int64()
	return v
}

func (r *ckRecord) flag(name string) bool { return r.num(name) == 1 }

func (r *ckRecord) bytes(name string) []byte {
	s := r.str(name)
	if s == "" {
		return nil
	}
	b, err := base64.StdEncoding.DecodeString(s)
	if err != nil {
		return nil
	}
	return b
}

func (r *ckRecord) resource(prefix string) *Resource {
	var v struct {
		DownloadURL  string `json:"downloadURL"`
		Size         int64  `json:"size"`
		FileChecksum string `json:"fileChecksum"`
	}
	if json.Unmarshal(r.field(prefix+"Res"), &v) != nil || v.DownloadURL == "" {
		return nil
	}
	return &Resource{URL: v.DownloadURL, Size: v.Size, Checksum: v.FileChecksum, FileType: r.str(prefix + "FileType")}
}

// parseAsset собирает Asset из пары записей CPLMaster + CPLAsset.
func parseAsset(master, asset *ckRecord) Asset {
	a := Asset{
		ID:        asset.RecordName,
		MasterID:  master.RecordName,
		ItemType:  master.str("itemType"),
		TZOffset:  int(asset.num("timeZoneOffset")),
		Favorite:  asset.flag("isFavorite"),
		Hidden:    asset.flag("isHidden"),
		Deleted:   asset.flag("isDeleted"),
		Original:  master.resource("resOriginal"),
		OrigLive:  master.resource("resOriginalVidCompl"),
		ChangeTag: master.RecordChangeTag + "/" + asset.RecordChangeTag,
	}
	if b := master.bytes("filenameEnc"); utf8.Valid(b) {
		a.Filename = string(b)
	}
	if ms := asset.num("assetDate"); ms != 0 {
		a.Date = time.UnixMilli(ms).UTC()
	}
	if a.ItemType == "" && a.Original != nil {
		a.ItemType = a.Original.FileType
	}
	a.Caption = decodeText(asset.bytes("captionEnc"))
	a.Location = decodeLocation(asset.bytes("locationEnc"))
	if a.Location == nil {
		a.Location = decodeLocation(asset.bytes("locationV2Enc"))
	}
	if asset.str("adjustmentType") != "" {
		if a.IsVideo() {
			a.Edited = asset.resource("resVidFull")
		} else {
			a.Edited = asset.resource("resJPEGFull")
			a.EditLive = asset.resource("resVidFull")
		}
		a.Adjusted = a.Edited != nil
	}
	return a
}

func decodeText(b []byte) string {
	if len(b) == 0 {
		return ""
	}
	if strings.HasPrefix(string(b), "bplist") {
		var s string
		if _, err := plist.Unmarshal(b, &s); err == nil {
			return strings.TrimSpace(s)
		}
		return ""
	}
	if !utf8.Valid(b) {
		return ""
	}
	return strings.TrimSpace(string(b))
}

// decodeLocation разбирает plist с координатами (обычный или NSKeyedArchiver).
func decodeLocation(b []byte) *Location {
	if len(b) == 0 {
		return nil
	}
	var v any
	if _, err := plist.Unmarshal(b, &v); err != nil {
		return nil
	}
	return findLocation(v, 0)
}

func findLocation(v any, depth int) *Location {
	if depth > 6 {
		return nil
	}
	switch t := v.(type) {
	case map[string]any:
		lat, okLat := pickFloat(t, "lat", "latitude", "kCLLocationCodingKeyCoordinateLatitude")
		lon, okLon := pickFloat(t, "lon", "lng", "longitude", "kCLLocationCodingKeyCoordinateLongitude")
		if okLat && okLon && !(lat == 0 && lon == 0) && lat >= -90 && lat <= 90 && lon >= -180 && lon <= 180 {
			loc := &Location{Lat: lat, Lon: lon}
			if alt, ok := pickFloat(t, "alt", "altitude", "kCLLocationCodingKeyAltitude"); ok {
				loc.Alt = &alt
			}
			return loc
		}
		for _, sub := range t {
			if l := findLocation(sub, depth+1); l != nil {
				return l
			}
		}
	case []any:
		for _, sub := range t {
			if l := findLocation(sub, depth+1); l != nil {
				return l
			}
		}
	}
	return nil
}

func pickFloat(m map[string]any, keys ...string) (float64, bool) {
	for _, k := range keys {
		switch n := m[k].(type) {
		case float64:
			return n, true
		case float32:
			return float64(n), true
		case int64:
			return float64(n), true
		case uint64:
			return float64(n), true
		}
	}
	return 0, false
}

var videoTypes = map[string]bool{
	"com.apple.quicktime-movie": true, "public.mpeg-4": true, "public.movie": true,
	"public.avi": true, "public.mpeg": true, "public.3gpp": true, "com.apple.m4v-video": true,
}

func isVideoType(t string) bool { return videoTypes[t] }

var typeExt = map[string]string{
	"public.jpeg": ".JPG", "public.heic": ".HEIC", "public.heif": ".HEIF", "public.png": ".PNG",
	"public.tiff": ".TIF", "com.compuserve.gif": ".GIF", "org.webmproject.webp": ".WEBP",
	"com.apple.quicktime-movie": ".MOV", "public.mpeg-4": ".MP4", "com.apple.m4v-video": ".M4V",
	"public.avi": ".AVI", "public.3gpp": ".3GP", "com.adobe.raw-image": ".DNG", "com.canon.cr2-raw-image": ".CR2",
	"com.canon.cr3-raw-image": ".CR3", "com.nikon.raw-image": ".NEF", "com.nikon.nrw-raw-image": ".NRW",
	"com.sony.arw-raw-image": ".ARW", "com.fuji.raw-image": ".RAF", "com.olympus.raw-image": ".ORF",
	"com.panasonic.rw2-raw-image": ".RW2", "public.avci": ".AVCI",
}

// ExtForType — расширение файла по UTI (пусто, если тип неизвестен).
func ExtForType(uti string) string { return typeExt[uti] }
