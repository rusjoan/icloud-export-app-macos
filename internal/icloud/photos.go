package icloud

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"
)

const primaryZone = "PrimarySync"

// Library — медиатека: личная (PrimarySync) или общая (SharedSync-…).
type Library struct {
	Zone   string         `json:"zone"`
	Shared bool           `json:"shared"`
	zoneID map[string]any // как его отдал CloudKit (с владельцем и типом зоны)
}

// ErrWebAccessDisabled — фото недоступны через веб (обычно включена ADP).
var ErrWebAccessDisabled = errors.New("iCloud Photos недоступны через веб. Если включена «Расширенная защита данных», " +
	"включите на iPhone: Настройки → [имя] → iCloud → «Доступ к данным iCloud через веб»")

// Photos — клиент CloudKit-базы com.apple.photos.cloud.
type Photos struct {
	s         *Session
	endpoint  string
	libraries []Library
}

func NewPhotos(ctx context.Context, s *Session) (*Photos, error) {
	acc := s.Account()
	if acc == nil {
		return nil, ErrNeedLogin
	}
	if acc.PhotosURL == "" {
		return nil, ErrWebAccessDisabled
	}
	p := &Photos{s: s, endpoint: strings.TrimRight(acc.PhotosURL, "/") + "/database/1/com.apple.photos.cloud/production/private"}
	libs, err := p.discoverLibraries(ctx)
	if err != nil {
		return nil, err
	}
	for _, lib := range libs {
		ready, err := p.indexed(ctx, lib)
		if err != nil {
			return nil, err
		}
		switch {
		case ready:
			p.libraries = append(p.libraries, lib)
		case !lib.Shared:
			return nil, errors.New("iCloud ещё индексирует медиатеку, попробуйте позже")
		}
		// общая медиатека ещё индексируется — просто не показываем её
	}
	return p, nil
}

// Libraries — доступные медиатеки (личная первой).
func (p *Photos) Libraries() []Library { return append([]Library(nil), p.libraries...) }

// discoverLibraries находит личную и общие медиатеки среди зон базы.
func (p *Photos) discoverLibraries(ctx context.Context) ([]Library, error) {
	var resp struct {
		Zones []struct {
			ZoneID  map[string]any `json:"zoneID"`
			Deleted bool           `json:"deleted"`
		} `json:"zones"`
	}
	if err := p.post(ctx, "/changes/database", map[string]any{}, &resp); err != nil {
		return nil, err
	}
	primary := Library{Zone: primaryZone, zoneID: map[string]any{"zoneName": primaryZone}}
	var shared []Library
	for _, z := range resp.Zones {
		name, _ := z.ZoneID["zoneName"].(string)
		switch {
		case z.Deleted:
		case name == primaryZone:
			primary.zoneID = z.ZoneID
		case strings.HasPrefix(name, "SharedSync-"):
			shared = append(shared, Library{Zone: name, Shared: true, zoneID: z.ZoneID})
		}
	}
	return append([]Library{primary}, shared...), nil
}

func (p *Photos) indexed(ctx context.Context, lib Library) (bool, error) {
	var resp struct {
		Records []ckRecord `json:"records"`
	}
	if err := p.post(ctx, "/records/query", map[string]any{
		"query":  map[string]any{"recordType": "CheckIndexingState"},
		"zoneID": lib.zoneID,
	}, &resp); err != nil {
		return false, err
	}
	if len(resp.Records) > 0 {
		if st := resp.Records[0].str("state"); st != "" && st != "FINISHED" {
			return false, nil
		}
	}
	return true, nil
}

func (p *Photos) zoneID(zone string) map[string]any {
	for _, l := range p.libraries {
		if l.Zone == zone {
			return l.zoneID
		}
	}
	return map[string]any{"zoneName": zone}
}

type smartDef struct {
	id, name, keyword, objType, listType, filter string
	kind                                         AlbumKind
}

var smartAlbums = []smartDef{
	{"all", "Все фото", "", "CPLAssetByAssetDateWithoutHiddenOrDeleted", "CPLAssetAndMasterByAssetDateWithoutHiddenOrDeleted", "", KindSmart},
	{"favorites", "Избранное", "", "CPLAssetInSmartAlbumByAssetDate:Favorite", "CPLAssetAndMasterInSmartAlbumByAssetDate", "FAVORITE", KindSmart},
	{"videos", "Видео", "", "CPLAssetInSmartAlbumByAssetDate:Video", "CPLAssetAndMasterInSmartAlbumByAssetDate", "VIDEO", KindSmart},
	{"live", "Live Photos", "", "CPLAssetInSmartAlbumByAssetDate:Live", "CPLAssetAndMasterInSmartAlbumByAssetDate", "LIVE", KindSmart},
	{"panoramas", "Панорамы", "", "CPLAssetInSmartAlbumByAssetDate:Panorama", "CPLAssetAndMasterInSmartAlbumByAssetDate", "PANORAMA", KindSmart},
	{"screenshots", "Снимки экрана", "", "CPLAssetInSmartAlbumByAssetDate:Screenshot", "CPLAssetAndMasterInSmartAlbumByAssetDate", "SCREENSHOT", KindSmart},
	{"slomo", "Замедленные", "", "CPLAssetInSmartAlbumByAssetDate:Slomo", "CPLAssetAndMasterInSmartAlbumByAssetDate", "SLOMO", KindSmart},
	{"timelapse", "Таймлапс", "", "CPLAssetInSmartAlbumByAssetDate:Timelapse", "CPLAssetAndMasterInSmartAlbumByAssetDate", "TIMELAPSE", KindSmart},
	{"bursts", "Серии", "", "CPLAssetBurstStackAssetByAssetDate", "CPLBurstStackAssetAndMasterByAssetDate", "", KindSmart},
	{"hidden", "Скрытые", "Hidden", "CPLAssetHiddenByAssetDate", "CPLAssetAndMasterHiddenByAssetDate", "", KindHidden},
	{"deleted", "Недавно удалённые", "Recently Deleted", "CPLAssetDeletedByExpungedDate", "CPLAssetAndMasterDeletedByExpungedDate", "", KindDeleted},
}

// SharedLibraryKeyword — ключевое слово для файлов из общей медиатеки.
const SharedLibraryKeyword = "Shared Library"

// Albums возвращает альбомы всех медиатек с количеством файлов.
func (p *Photos) Albums(ctx context.Context) ([]Album, error) {
	var out []Album
	for _, lib := range p.libraries {
		albums, err := p.libraryAlbums(ctx, lib)
		if err != nil {
			return nil, err
		}
		out = append(out, albums...)
	}
	if err := p.fillCounts(ctx, out); err != nil {
		return nil, err
	}
	return out, nil
}

func (p *Photos) libraryAlbums(ctx context.Context, lib Library) ([]Album, error) {
	var out []Album
	// у общей медиатеки свои ID, чтобы не путать с одноимёнными альбомами личной
	prefix, libKeyword := "", ""
	if lib.Shared {
		prefix, libKeyword = lib.Zone+"/", SharedLibraryKeyword
	}
	for _, d := range smartAlbums {
		a := Album{ID: prefix + "smart:" + d.id, Name: d.name, Kind: d.kind, Keyword: d.keyword, Library: lib.Zone,
			Shared: lib.Shared, LibraryKeyword: libKeyword, listType: d.listType, objType: d.objType}
		if d.filter != "" {
			a.filters = []map[string]any{eqFilter("smartAlbum", "STRING", d.filter)}
		}
		out = append(out, a)
	}

	var marker any
	for {
		body := map[string]any{
			"query":  map[string]any{"recordType": "CPLAlbumByPositionLive"},
			"zoneID": lib.zoneID,
		}
		if marker != nil {
			body["continuationMarker"] = marker
		}
		var resp struct {
			Records            []ckRecord `json:"records"`
			ContinuationMarker any        `json:"continuationMarker"`
		}
		if err := p.post(ctx, "/records/query", body, &resp); err != nil {
			return nil, err
		}
		for i := range resp.Records {
			r := &resp.Records[i]
			if r.RecordName == "----Root-Folder----" || r.RecordName == "----Project-Root-Folder----" || r.flag("isDeleted") {
				continue
			}
			name := decodeText(r.bytes("albumNameEnc"))
			if name == "" {
				name = "Без названия"
			}
			a := Album{ID: prefix + r.RecordName, Name: name, Keyword: name, Kind: KindUser, ParentID: r.str("parentId"),
				Library: lib.Zone, Shared: lib.Shared, LibraryKeyword: libKeyword}
			if a.ParentID == "----Root-Folder----" {
				a.ParentID = ""
			} else if a.ParentID != "" {
				a.ParentID = prefix + a.ParentID
			}
			if r.num("albumType") == 3 {
				a.Kind = KindFolder
			} else {
				a.listType = "CPLContainerRelationLiveByAssetDate"
				a.objType = "CPLContainerRelationNotDeletedByAssetDate:" + r.RecordName
				a.filters = []map[string]any{eqFilter("parentId", "STRING", r.RecordName)}
			}
			out = append(out, a)
		}
		if resp.ContinuationMarker == nil || len(resp.Records) == 0 {
			break
		}
		marker = resp.ContinuationMarker
	}
	return out, nil
}

func (p *Photos) fillCounts(ctx context.Context, albums []Album) error {
	var idx []int
	var batch []map[string]any
	for i, a := range albums {
		if a.objType == "" {
			continue
		}
		idx = append(idx, i)
		batch = append(batch, map[string]any{
			"resultsLimit": 1,
			"query": map[string]any{
				"recordType": "HyperionIndexCountLookup",
				"filterBy": map[string]any{
					"fieldName":  "indexCountID",
					"comparator": "IN",
					"fieldValue": map[string]any{"type": "STRING_LIST", "value": []string{a.objType}},
				},
			},
			"zoneWide": true,
			"zoneID":   p.zoneID(a.Library),
		})
	}
	for start := 0; start < len(batch); start += 50 {
		end := min(start+50, len(batch))
		var resp struct {
			Batch []struct {
				Records []ckRecord `json:"records"`
			} `json:"batch"`
		}
		if err := p.post(ctx, "/internal/records/query/batch", map[string]any{"batch": batch[start:end]}, &resp); err != nil {
			return err
		}
		for j, b := range resp.Batch {
			if len(b.Records) > 0 && start+j < len(idx) {
				albums[idx[start+j]].Count = int(b.Records[0].num("itemCount"))
			}
		}
	}
	return nil
}

// Assets перебирает все файлы альбома.
func (p *Photos) Assets(ctx context.Context, album Album, fn func(Asset) error) error {
	if album.listType == "" {
		return nil
	}
	offset := 0
	for {
		filters := []map[string]any{
			eqFilter("startRank", "INT64", offset),
			eqFilter("direction", "STRING", "ASCENDING"),
		}
		filters = append(filters, album.filters...)
		var resp struct {
			Records []ckRecord `json:"records"`
		}
		if err := p.post(ctx, "/records/query", map[string]any{
			"query":        map[string]any{"recordType": album.listType, "filterBy": filters},
			"resultsLimit": 200,
			"zoneID":       p.zoneID(album.Library),
		}, &resp); err != nil {
			return err
		}
		assets := map[string]*ckRecord{}
		var masters []*ckRecord
		for i := range resp.Records {
			r := &resp.Records[i]
			switch r.RecordType {
			case "CPLAsset":
				var ref struct {
					RecordName string `json:"recordName"`
				}
				_ = json.Unmarshal(r.field("masterRef"), &ref)
				assets[ref.RecordName] = r
			case "CPLMaster":
				masters = append(masters, r)
			}
		}
		if len(masters) == 0 {
			return nil
		}
		offset += len(masters)
		for _, m := range masters {
			a, ok := assets[m.RecordName]
			if !ok {
				continue
			}
			asset := parseAsset(m, a)
			asset.Zone = album.Library
			if err := fn(asset); err != nil {
				return err
			}
		}
	}
}

// Refresh перечитывает записи ассета — нужны свежие ссылки на загрузку.
func (p *Photos) Refresh(ctx context.Context, a Asset) (Asset, error) {
	var resp struct {
		Records []ckRecord `json:"records"`
	}
	if err := p.post(ctx, "/records/lookup", map[string]any{
		"records": []map[string]any{{"recordName": a.MasterID}, {"recordName": a.ID}},
		"zoneID":  p.zoneID(a.Zone),
	}, &resp); err != nil {
		return a, err
	}
	var master, asset *ckRecord
	for i := range resp.Records {
		switch r := &resp.Records[i]; r.RecordName {
		case a.MasterID:
			master = r
		case a.ID:
			asset = r
		}
	}
	if master == nil || asset == nil || master.ServerErrorCode != "" || asset.ServerErrorCode != "" {
		return a, fmt.Errorf("запись %s больше не доступна в iCloud", a.ID)
	}
	fresh := parseAsset(master, asset)
	fresh.Zone = a.Zone
	return fresh, nil
}

func eqFilter(field, typ string, value any) map[string]any {
	return map[string]any{
		"fieldName":  field,
		"comparator": "EQUALS",
		"fieldValue": map[string]any{"type": typ, "value": value},
	}
}

func (p *Photos) post(ctx context.Context, path string, body, out any) error {
	data, _ := json.Marshal(body)
	for attempt := 0; ; attempt++ {
		params := p.s.ClientParams()
		params.Set("remapEnums", "true")
		params.Set("getCurrentSyncToken", "true")
		req, err := http.NewRequestWithContext(ctx, http.MethodPost, p.endpoint+path+"?"+params.Encode(), bytes.NewReader(data))
		if err != nil {
			return err
		}
		p.s.WebHeaders(req)
		start := time.Now()
		resp, err := p.s.HTTP().Do(req)
		if err != nil {
			Logf("cloudkit POST %s: %v", path, err)
			return err
		}
		Logf("cloudkit POST %s → %d (%s)", path, resp.StatusCode, time.Since(start).Round(time.Millisecond))
		raw, err := io.ReadAll(io.LimitReader(resp.Body, 64<<20))
		resp.Body.Close()
		if err != nil {
			return err
		}
		switch {
		case resp.StatusCode == 401 || resp.StatusCode == 421:
			if attempt == 0 && p.s.Revalidate(ctx) == nil {
				continue
			}
			return ErrNeedLogin
		case resp.StatusCode >= 300:
			if bytes.Contains(raw, []byte("ZONE_NOT_FOUND")) || bytes.Contains(raw, []byte("ACCESS_DENIED")) ||
				bytes.Contains(raw, []byte("PRIVATE_DB_DISABLED")) {
				return ErrWebAccessDisabled
			}
			return fmt.Errorf("CloudKit %s: HTTP %d: %s", path, resp.StatusCode, truncate(raw, 300))
		}
		_ = p.s.Save()
		return json.Unmarshal(raw, out)
	}
}

func truncate(b []byte, n int) string {
	if len(b) > n {
		return string(b[:n]) + "…"
	}
	return string(b)
}
