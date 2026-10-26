package icloud

import (
	"encoding/base64"
	"encoding/json"
	"fmt"
	"testing"
	"time"

	"howett.net/plist"
)

func rec(t *testing.T, s string) *ckRecord {
	t.Helper()
	var r ckRecord
	if err := json.Unmarshal([]byte(s), &r); err != nil {
		t.Fatal(err)
	}
	return &r
}

func b64(s []byte) string { return base64.StdEncoding.EncodeToString(s) }

func TestParseAssetEditedLivePhoto(t *testing.T) {
	loc, err := plist.Marshal(map[string]any{"lat": 55.75, "lon": 37.61, "alt": 150.5, "timestamp": 1.0}, plist.BinaryFormat)
	if err != nil {
		t.Fatal(err)
	}
	master := rec(t, fmt.Sprintf(`{"recordName":"M1","recordType":"CPLMaster","recordChangeTag":"m7","fields":{
		"filenameEnc":{"value":%q,"type":"ENCRYPTED_BYTES"},
		"itemType":{"value":"public.heic","type":"STRING"},
		"resOriginalRes":{"value":{"downloadURL":"https://cvws/orig","size":1000,"fileChecksum":"c1"},"type":"ASSETID"},
		"resOriginalFileType":{"value":"public.heic","type":"STRING"},
		"resOriginalVidComplRes":{"value":{"downloadURL":"https://cvws/live","size":500},"type":"ASSETID"},
		"resOriginalVidComplFileType":{"value":"com.apple.quicktime-movie","type":"STRING"}}}`, b64([]byte("IMG_0001.HEIC"))))
	asset := rec(t, fmt.Sprintf(`{"recordName":"A1","recordType":"CPLAsset","recordChangeTag":"a3","fields":{
		"assetDate":{"value":1718000000000,"type":"TIMESTAMP"},
		"timeZoneOffset":{"value":10800,"type":"INT64"},
		"isFavorite":{"value":1,"type":"INT64"},
		"isHidden":{"value":1,"type":"INT64"},
		"captionEnc":{"value":%q,"type":"ENCRYPTED_BYTES"},
		"locationEnc":{"value":%q,"type":"ENCRYPTED_BYTES"},
		"adjustmentType":{"value":"com.apple.photo","type":"STRING"},
		"resJPEGFullRes":{"value":{"downloadURL":"https://cvws/edit","size":900},"type":"ASSETID"},
		"resJPEGFullFileType":{"value":"public.jpeg","type":"STRING"},
		"resVidFullRes":{"value":{"downloadURL":"https://cvws/editlive","size":400},"type":"ASSETID"},
		"resVidFullFileType":{"value":"com.apple.quicktime-movie","type":"STRING"},
		"masterRef":{"value":{"recordName":"M1"},"type":"REFERENCE"}}}`, b64([]byte("Закат")), b64(loc)))

	a := parseAsset(master, asset)
	if a.Filename != "IMG_0001.HEIC" || a.IsVideo() || !a.Favorite || !a.Hidden || a.Deleted {
		t.Fatalf("basic fields wrong: %+v", a)
	}
	if !a.Date.Equal(time.UnixMilli(1718000000000)) || a.LocalDate().Format("-07:00") != "+03:00" {
		t.Fatalf("date wrong: %v %v", a.Date, a.LocalDate())
	}
	if a.Caption != "Закат" {
		t.Fatalf("caption = %q", a.Caption)
	}
	if a.Location == nil || a.Location.Lat != 55.75 || a.Location.Lon != 37.61 || a.Location.Alt == nil || *a.Location.Alt != 150.5 {
		t.Fatalf("location = %+v", a.Location)
	}
	if !a.Adjusted || a.Edited.URL != "https://cvws/edit" || a.Edited.FileType != "public.jpeg" || a.EditLive.URL != "https://cvws/editlive" {
		t.Fatalf("edited wrong: %+v %+v", a.Edited, a.EditLive)
	}
	if a.Original.FileType != "public.heic" || a.OrigLive.FileType != "com.apple.quicktime-movie" || a.ChangeTag != "m7/a3" {
		t.Fatalf("original wrong: %+v %+v %s", a.Original, a.OrigLive, a.ChangeTag)
	}
}

func TestParseAssetEditedVideoUsesVidFull(t *testing.T) {
	master := rec(t, `{"recordName":"M2","recordType":"CPLMaster","fields":{
		"itemType":{"value":"com.apple.quicktime-movie"},
		"resOriginalRes":{"value":{"downloadURL":"https://o","size":1}}}}`)
	asset := rec(t, `{"recordName":"A2","recordType":"CPLAsset","fields":{
		"adjustmentType":{"value":"com.apple.video.slomo"},
		"resVidFullRes":{"value":{"downloadURL":"https://e","size":1}},
		"resVidFullFileType":{"value":"com.apple.quicktime-movie"}}}`)
	a := parseAsset(master, asset)
	if !a.IsVideo() || !a.Adjusted || a.Edited.URL != "https://e" || a.EditLive != nil {
		t.Fatalf("video edit wrong: %+v", a)
	}
}

func TestParseAssetNotAdjustedIgnoresStrayRender(t *testing.T) {
	master := rec(t, `{"recordName":"M3","recordType":"CPLMaster","fields":{"itemType":{"value":"public.jpeg"},
		"resOriginalRes":{"value":{"downloadURL":"https://o","size":1}}}}`)
	asset := rec(t, `{"recordName":"A3","recordType":"CPLAsset","fields":{
		"resJPEGFullRes":{"value":{"downloadURL":"https://e","size":1}}}}`)
	if a := parseAsset(master, asset); a.Adjusted || a.Edited != nil {
		t.Fatalf("unadjusted asset must not use render: %+v", a)
	}
}

func TestDecodeLocationKeyedArchiverAndGarbage(t *testing.T) {
	archived, _ := plist.Marshal(map[string]any{
		"$archiver": "NSKeyedArchiver",
		"$objects":  []any{"$null", map[string]any{"kCLLocationCodingKeyCoordinateLatitude": -33.9, "kCLLocationCodingKeyCoordinateLongitude": 151.2}},
	}, plist.BinaryFormat)
	if l := decodeLocation(archived); l == nil || l.Lat != -33.9 || l.Lon != 151.2 || l.Alt != nil {
		t.Fatalf("archived location = %+v", l)
	}
	if l := decodeLocation([]byte("not a plist")); l != nil {
		t.Fatalf("garbage must give nil, got %+v", l)
	}
}
