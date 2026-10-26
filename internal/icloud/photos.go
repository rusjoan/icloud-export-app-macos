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
)

const zone = "PrimarySync"

// ErrWebAccessDisabled — фото недоступны через веб (обычно включена ADP).
var ErrWebAccessDisabled = errors.New("iCloud Photos недоступны через веб. Если включена «Расширенная защита данных», " +
	"включите на iPhone: Настройки → [имя] → iCloud → «Доступ к данным iCloud через веб»")

// Photos — клиент CloudKit-базы com.apple.photos.cloud.
type Photos struct {
	s        *Session
	endpoint string
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
	var resp struct {
		Records []ckRecord `json:"records"`
	}
	if err := p.post(ctx, "/records/query", map[string]any{
		"query":  map[string]any{"recordType": "CheckIndexingState"},
		"zoneID": map[string]any{"zoneName": zone},
	}, &resp); err != nil {
		return nil, err
	}
	if len(resp.Records) > 0 {
		if st := resp.Records[0].str("state"); st != "" && st != "FINISHED" {
			return nil, errors.New("iCloud ещё индексирует медиатеку, попробуйте позже")
		}
	}
	return p, nil
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

// Albums возвращает смарт-альбомы и пользовательские альбомы с количеством файлов.
func (p *Photos) Albums(ctx context.Context) ([]Album, error) {
	var out []Album
	for _, d := range smartAlbums {
		a := Album{ID: "smart:" + d.id, Name: d.name, Kind: d.kind, Keyword: d.keyword, listType: d.listType, objType: d.objType}
		if d.filter != "" {
			a.filters = []map[string]any{eqFilter("smartAlbum", "STRING", d.filter)}
		}
		out = append(out, a)
	}

	var marker any
	for {
		body := map[string]any{
			"query":  map[string]any{"recordType": "CPLAlbumByPositionLive"},
			"zoneID": map[string]any{"zoneName": zone},
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
			a := Album{ID: r.RecordName, Name: name, Keyword: name, Kind: KindUser, ParentID: r.str("parentId")}
			if a.ParentID == "----Root-Folder----" {
				a.ParentID = ""
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
	if err := p.fillCounts(ctx, out); err != nil {
		return nil, err
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
			"zoneID":   map[string]any{"zoneName": zone},
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
			"zoneID":       map[string]any{"zoneName": zone},
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
			if err := fn(parseAsset(m, a)); err != nil {
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
		"zoneID":  map[string]any{"zoneName": zone},
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
	return parseAsset(master, asset), nil
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
		resp, err := p.s.HTTP().Do(req)
		if err != nil {
			return err
		}
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
