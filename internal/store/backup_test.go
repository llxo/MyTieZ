package store

import (
	"errors"
	"image/color"
	"os"
	"path/filepath"
	"testing"
)

func TestBackupZipRoundTripAndDeduplication(t *testing.T) {
	srcDir := t.TempDir()
	srcStore, err := OpenStore(srcDir)
	if err != nil {
		t.Fatalf("open src store: %v", err)
	}
	defer srcStore.Close()

	txtSaved, err := srcStore.UpsertText("hello backup")
	if err != nil {
		t.Fatalf("save text: %v", err)
	}
	if _, err := srcStore.UpdateTags(txtSaved.ID, []string{"work"}); err != nil {
		t.Fatalf("tag text: %v", err)
	}

	sensitiveSaved, err := srcStore.UpsertText("sk-secret-token-123456")
	if err != nil {
		t.Fatalf("save sensitive: %v", err)
	}
	if _, err := srcStore.UpdateTags(sensitiveSaved.ID, []string{SensitiveTag}); err != nil {
		t.Fatalf("tag sensitive: %v", err)
	}
	if _, err := srcStore.TogglePin(sensitiveSaved.ID); err != nil {
		t.Fatalf("pin sensitive: %v", err)
	}

	pngBytes := makeTestPNG(t, color.RGBA{R: 12, G: 180, B: 90, A: 255})
	if _, err := srcStore.UpsertImage(pngBytes); err != nil {
		t.Fatalf("save image: %v", err)
	}

	zipPath := filepath.Join(t.TempDir(), "backup.zip")
	exp, err := srcStore.ExportBackupZip(zipPath, "")
	if err != nil {
		t.Fatalf("export backup: %v", err)
	}
	if exp.ItemCount != 3 || exp.ImageCount != 1 || exp.Encrypted {
		t.Fatalf("unexpected export result: %+v", exp)
	}

	dstDir := t.TempDir()
	dstStore, err := OpenStore(dstDir)
	if err != nil {
		t.Fatalf("open dst store: %v", err)
	}
	defer dstStore.Close()

	imp, err := dstStore.ImportBackupZip(zipPath, "")
	if err != nil {
		t.Fatalf("import backup: %v", err)
	}
	if imp.Imported != 3 || imp.Skipped != 0 || imp.Images != 1 {
		t.Fatalf("unexpected import result: %+v", imp)
	}

	// Re-importing the same archive should deduplicate all 3 items.
	imp2, err := dstStore.ImportBackupZip(zipPath, "")
	if err != nil {
		t.Fatalf("re-import backup: %v", err)
	}
	if imp2.Imported != 0 || imp2.Skipped != 3 || imp2.Images != 0 {
		t.Fatalf("unexpected re-import result: %+v", imp2)
	}

	items, err := dstStore.ListSummaries("", "", 20)
	if err != nil {
		t.Fatalf("list imported items: %v", err)
	}
	if len(items) != 3 {
		t.Fatalf("expected 3 items, got %d", len(items))
	}

	var gotSecret, gotImage ClipItem
	for _, it := range items {
		full, err := dstStore.GetByID(it.ID)
		if err != nil {
			t.Fatalf("get by id %d: %v", it.ID, err)
		}
		if full.Text == "sk-secret-token-123456" {
			gotSecret = full
		}
		if full.Kind == KindImage {
			gotImage = full
		}
	}
	if gotSecret.Text != "sk-secret-token-123456" || !gotSecret.Pinned || !HasSensitiveTag(gotSecret.Tags) {
		t.Fatalf("unexpected restored secret item: %+v", gotSecret)
	}
	if filepath.Dir(gotImage.ImagePath) != dstStore.imageDir {
		t.Fatalf("expected image path in dst images dir %q, got %q", dstStore.imageDir, gotImage.ImagePath)
	}
	gotImgBytes, err := os.ReadFile(gotImage.ImagePath)
	if err != nil || string(gotImgBytes) != string(pngBytes) {
		t.Fatalf("restored image mismatch: err=%v len=%d", err, len(gotImgBytes))
	}
}

func TestBackupZipEncryptedPasswordFlow(t *testing.T) {
	srcStore, err := OpenStore(t.TempDir())
	if err != nil {
		t.Fatalf("open src store: %v", err)
	}
	defer srcStore.Close()

	if _, err := srcStore.UpsertText("encrypted payload text"); err != nil {
		t.Fatalf("save item: %v", err)
	}

	zipPath := filepath.Join(t.TempDir(), "encrypted.zip")
	exp, err := srcStore.ExportBackupZip(zipPath, "my-strong-pass")
	if err != nil {
		t.Fatalf("export encrypted: %v", err)
	}
	if !exp.Encrypted || exp.ItemCount != 1 {
		t.Fatalf("unexpected encrypted export result: %+v", exp)
	}

	manifest, err := InspectBackupZip(zipPath)
	if err != nil {
		t.Fatalf("inspect backup: %v", err)
	}
	if !manifest.Encrypted || manifest.ItemCount != 1 {
		t.Fatalf("unexpected manifest: %+v", manifest)
	}

	dstStore, err := OpenStore(t.TempDir())
	if err != nil {
		t.Fatalf("open dst store: %v", err)
	}
	defer dstStore.Close()

	if _, err := dstStore.ImportBackupZip(zipPath, ""); !errors.Is(err, ErrBackupPasswordRequired) {
		t.Fatalf("expected ErrBackupPasswordRequired, got %v", err)
	}
	if _, err := dstStore.ImportBackupZip(zipPath, "wrong-pass"); !errors.Is(err, ErrBackupInvalidPassword) {
		t.Fatalf("expected ErrBackupInvalidPassword, got %v", err)
	}

	imp, err := dstStore.ImportBackupZip(zipPath, "my-strong-pass")
	if err != nil {
		t.Fatalf("import with right password: %v", err)
	}
	if imp.Imported != 1 {
		t.Fatalf("expected 1 imported, got %+v", imp)
	}
}
