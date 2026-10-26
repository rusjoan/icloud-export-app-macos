package state

import (
	"os"
	"path/filepath"
	"testing"
)

func TestIndexRoundTripAndClaims(t *testing.T) {
	root := t.TempDir()
	ix, err := Open(root)
	if err != nil {
		t.Fatal(err)
	}
	if ix.Layout() != "" || ix.Len() != 0 {
		t.Fatal("new index must be empty")
	}
	ix.SetLayout("date")
	if !ix.Claim("A", "2024/06/IMG_1.HEIC", "2024/06/IMG_1.MOV") {
		t.Fatal("claim on free paths failed")
	}
	if ix.Claim("B", "2024/06/img_1.heic") {
		t.Fatal("case-insensitive collision not detected")
	}
	if err := ix.Put("A", Entry{Path: "2024/06/IMG_1.HEIC", LivePath: "2024/06/IMG_1.MOV", Size: 10, ChangeTag: "t1"}); err != nil {
		t.Fatal(err)
	}
	ix.Claim("B", "2024/06/IMG_2.HEIC")
	ix.Release("B", "2024/06/IMG_2.HEIC")
	if ix.Owner("2024/06/IMG_2.HEIC") != "" {
		t.Fatal("release did not free path")
	}
	if err := ix.Save(); err != nil {
		t.Fatal(err)
	}

	ix2, err := Open(root)
	if err != nil {
		t.Fatal(err)
	}
	e, ok := ix2.Get("A")
	if !ok || e.Size != 10 || ix2.Layout() != "date" || ix2.Owner("2024/06/IMG_1.MOV") != "A" {
		t.Fatalf("reloaded index wrong: %+v", e)
	}
}

func TestPutMovesPaths(t *testing.T) {
	ix, _ := Open(t.TempDir())
	_ = ix.Put("A", Entry{Path: "a.HEIC"})
	_ = ix.Put("A", Entry{Path: "a.JPG"})
	if ix.Owner("a.HEIC") != "" || ix.Owner("a.JPG") != "A" {
		t.Fatal("old path must be freed after re-put")
	}
}

func TestCorruptIndexIsError(t *testing.T) {
	root := t.TempDir()
	_ = os.WriteFile(filepath.Join(root, FileName), []byte("{broken"), 0o644)
	if _, err := Open(root); err == nil {
		t.Fatal("corrupt index must not be silently replaced")
	}
}
