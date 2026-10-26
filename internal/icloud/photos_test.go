package icloud

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"sync"
	"testing"
)

const sharedZone = "SharedSync-1234-ABCD"

// fakeCloudKit отвечает на запросы клиента Photos и запоминает, в какие зоны они шли.
type fakeCloudKit struct {
	mu    sync.Mutex
	zones []map[string]any // zoneID каждого запроса
}

func (f *fakeCloudKit) handler(t *testing.T) http.Handler {
	const base = "/database/1/com.apple.photos.cloud/production/private"
	mux := http.NewServeMux()
	record := func(z map[string]any) {
		f.mu.Lock()
		f.zones = append(f.zones, z)
		f.mu.Unlock()
	}
	mux.HandleFunc("POST "+base+"/changes/database", func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"zones":[
			{"zoneID":{"zoneName":"PrimarySync","ownerRecordName":"_me","zoneType":"REGULAR_CUSTOM_ZONE"}},
			{"zoneID":{"zoneName":"` + sharedZone + `","ownerRecordName":"_owner","zoneType":"REGULAR_CUSTOM_ZONE"}},
			{"zoneID":{"zoneName":"SharedSync-OLD"},"deleted":true},
			{"zoneID":{"zoneName":"CMM-Zone"}}]}`))
	})
	mux.HandleFunc("POST "+base+"/records/query", func(w http.ResponseWriter, r *http.Request) {
		var req struct {
			Query struct {
				RecordType string `json:"recordType"`
			} `json:"query"`
			ZoneID map[string]any `json:"zoneID"`
		}
		raw, _ := io.ReadAll(r.Body)
		_ = json.Unmarshal(raw, &req)
		record(req.ZoneID)
		zone, _ := req.ZoneID["zoneName"].(string)
		firstPage := strings.Contains(string(raw), `"fieldName":"startRank","fieldValue":{"type":"INT64","value":0}`)
		switch rt := req.Query.RecordType; {
		case rt == "CheckIndexingState":
			_, _ = w.Write([]byte(`{"records":[{"recordName":"x","fields":{"state":{"value":"FINISHED"}}}]}`))
		case rt == "CPLAlbumByPositionLive" && zone == "PrimarySync":
			name := base64.StdEncoding.EncodeToString([]byte("Отпуск"))
			_, _ = w.Write([]byte(`{"records":[
				{"recordName":"----Root-Folder----","fields":{}},
				{"recordName":"ALB1","fields":{"albumNameEnc":{"value":"` + name + `"},"parentId":{"value":"----Root-Folder----"}}}]}`))
		case rt == "CPLAlbumByPositionLive":
			_, _ = w.Write([]byte(`{"records":[]}`))
		case strings.HasPrefix(rt, "CPLAssetAndMaster") && zone == sharedZone && firstPage:
			_, _ = w.Write([]byte(`{"records":[
				{"recordName":"M9","recordType":"CPLMaster","fields":{"itemType":{"value":"public.jpeg"},
					"resOriginalRes":{"value":{"downloadURL":"https://cvws/x","size":3}}}},
				{"recordName":"A9","recordType":"CPLAsset","fields":{"masterRef":{"value":{"recordName":"M9"}}}}]}`))
		default:
			_, _ = w.Write([]byte(`{"records":[]}`))
		}
	})
	mux.HandleFunc("POST "+base+"/internal/records/query/batch", func(w http.ResponseWriter, r *http.Request) {
		var req struct {
			Batch []struct {
				ZoneID map[string]any `json:"zoneID"`
			} `json:"batch"`
		}
		_ = json.NewDecoder(r.Body).Decode(&req)
		var out []string
		for _, b := range req.Batch {
			record(b.ZoneID)
			n := "5"
			if b.ZoneID["zoneName"] == sharedZone {
				n = "7"
			}
			out = append(out, `{"records":[{"fields":{"itemCount":{"value":`+n+`}}}]}`)
		}
		_, _ = w.Write([]byte(`{"batch":[` + strings.Join(out, ",") + `]}`))
	})
	mux.HandleFunc("POST "+base+"/records/lookup", func(w http.ResponseWriter, r *http.Request) {
		var req struct {
			ZoneID map[string]any `json:"zoneID"`
		}
		_ = json.NewDecoder(r.Body).Decode(&req)
		record(req.ZoneID)
		_, _ = w.Write([]byte(`{"records":[
			{"recordName":"M9","recordType":"CPLMaster","fields":{"itemType":{"value":"public.jpeg"},
				"resOriginalRes":{"value":{"downloadURL":"https://cvws/fresh","size":3}}}},
			{"recordName":"A9","recordType":"CPLAsset","fields":{}}]}`))
	})
	return mux
}

func TestPhotosSharedLibrary(t *testing.T) {
	f := &fakeCloudKit{}
	srv := httptest.NewServer(f.handler(t))
	defer srv.Close()
	s := NewSession(filepath.Join(t.TempDir(), "s.json"))
	s.account = &Account{DSID: "1", PhotosURL: srv.URL}
	ctx := context.Background()

	p, err := NewPhotos(ctx, s)
	if err != nil {
		t.Fatal(err)
	}
	libs := p.Libraries()
	if len(libs) != 2 || libs[0].Zone != "PrimarySync" || libs[0].Shared || libs[1].Zone != sharedZone || !libs[1].Shared {
		t.Fatalf("libraries = %+v", libs)
	}

	albums, err := p.Albums(ctx)
	if err != nil {
		t.Fatal(err)
	}
	byID := map[string]Album{}
	for _, a := range albums {
		if _, dup := byID[a.ID]; dup {
			t.Fatalf("duplicate album id %s", a.ID)
		}
		byID[a.ID] = a
	}
	if a := byID["smart:all"]; a.Shared || a.Count != 5 || a.LibraryKeyword != "" {
		t.Fatalf("primary all = %+v", a)
	}
	if a := byID["ALB1"]; a.Name != "Отпуск" || a.ParentID != "" || a.Library != "PrimarySync" {
		t.Fatalf("user album = %+v", a)
	}
	shAll, ok := byID[sharedZone+"/smart:all"]
	if !ok || !shAll.Shared || shAll.Count != 7 || shAll.LibraryKeyword != SharedLibraryKeyword {
		t.Fatalf("shared all = %+v", shAll)
	}
	if _, ok := byID[sharedZone+"/smart:hidden"]; !ok {
		t.Fatal("shared library must have its own Hidden album")
	}

	var got []Asset
	if err := p.Assets(ctx, shAll, func(a Asset) error { got = append(got, a); return nil }); err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 || got[0].Zone != sharedZone {
		t.Fatalf("assets = %+v", got)
	}
	fresh, err := p.Refresh(ctx, got[0])
	if err != nil || fresh.Zone != sharedZone || fresh.Original.URL != "https://cvws/fresh" {
		t.Fatalf("refresh = %+v, %v", fresh, err)
	}

	// запросы к общей медиатеке идут с полным zoneID (владелец зоны обязателен)
	sharedCalls := 0
	for _, z := range f.zones {
		if z["zoneName"] == sharedZone {
			sharedCalls++
			if z["ownerRecordName"] != "_owner" {
				t.Fatalf("shared zone without owner: %v", z)
			}
		}
		if z["zoneName"] == "SharedSync-OLD" || z["zoneName"] == "CMM-Zone" {
			t.Fatalf("unexpected zone queried: %v", z)
		}
	}
	if sharedCalls == 0 {
		t.Fatal("shared zone never queried")
	}
}
