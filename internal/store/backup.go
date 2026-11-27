package store

import (
	"archive/zip"
	"bytes"
	"crypto/aes"
	"crypto/cipher"
	"crypto/pbkdf2"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path"
	"path/filepath"
	"strings"
	"time"
)

const (
	backupFormatVersion  = 1
	backupManifestName   = "manifest.json"
	backupItemsName      = "items.json"
	backupPayloadName    = "payload.enc"
	backupImagesDir      = "images"
	backupPayloadAAD     = "mytiez-backup-v1"
	backupPBKDF2Iters    = 100_000
	backupMaxManifest    = 1 << 20   // 1 MiB
	backupMaxItemsJSON   = 128 << 20 // 128 MiB
	backupMaxImageBytes  = 64 << 20  // 64 MiB
	backupMaxPayloadSize = 512 << 20 // 512 MiB
)

var (
	ErrBackupPasswordRequired = errors.New("backup password required")
	ErrBackupInvalidPassword  = errors.New("invalid backup password")
	ErrBackupInvalidFormat    = errors.New("invalid backup format")
)

type BackupManifest struct {
	FormatVersion int       `json:"format_version"`
	ExportedAt    time.Time `json:"exported_at"`
	Encrypted     bool      `json:"encrypted"`
	Salt          string    `json:"salt,omitempty"`
	Nonce         string    `json:"nonce,omitempty"`
	ItemCount     int       `json:"item_count"`
	ImageCount    int       `json:"image_count"`
}

type BackupItem struct {
	Kind       ClipKind  `json:"kind"`
	Hash       string    `json:"hash"`
	Text       string    `json:"text,omitempty"`
	HTML       string    `json:"html,omitempty"`
	ImageFile  string    `json:"image_file,omitempty"`
	Files      []string  `json:"files,omitempty"`
	Preview    string    `json:"preview,omitempty"`
	SourceApp  string    `json:"source_app,omitempty"`
	SourcePath string    `json:"source_path,omitempty"`
	ByteSize   int64     `json:"byte_size,omitempty"`
	Pinned     bool      `json:"pinned,omitempty"`
	Tags       []string  `json:"tags,omitempty"`
	CreatedAt  time.Time `json:"created_at"`
	UpdatedAt  time.Time `json:"updated_at"`
}

type ExportResult struct {
	ItemCount  int
	ImageCount int
	Encrypted  bool
}

type ImportResult struct {
	Imported int
	Skipped  int
	Images   int
}

// InspectBackupZip reads and validates manifest.json from a backup zip without decrypting payload.
func InspectBackupZip(zipPath string) (BackupManifest, error) {
	zr, err := zip.OpenReader(zipPath)
	if err != nil {
		return BackupManifest{}, fmt.Errorf("%w: %v", ErrBackupInvalidFormat, err)
	}
	defer zr.Close()
	return readManifestFromZip(&zr.Reader)
}

// ExportBackupZip exports all clipboard items and referenced images into a zip archive.
// If password is non-empty, the inner payload (items.json and images/) is encrypted with AES-256-GCM.
func (s *Store) ExportBackupZip(zipPath, password string) (ExportResult, error) {
	if s == nil || s.db == nil {
		return ExportResult{}, errors.New("store is not initialized")
	}
	items, err := s.loadAllItemsForBackup()
	if err != nil {
		return ExportResult{}, err
	}

	backupItems := make([]BackupItem, 0, len(items))
	usedImageNames := make(map[string]string) // srcPath -> archive rel path ("images/...")
	var imageFiles []struct {
		srcPath string
		relPath string
	}

	for _, it := range items {
		bi := BackupItem{
			Kind:       it.Kind,
			Hash:       it.Hash,
			Text:       it.Text,
			HTML:       it.HTML,
			Files:      it.Files,
			Preview:    it.Preview,
			SourceApp:  it.SourceApp,
			SourcePath: it.SourcePath,
			ByteSize:   it.ByteSize,
			Pinned:     it.Pinned,
			Tags:       it.Tags,
			CreatedAt:  it.CreatedAt,
			UpdatedAt:  it.UpdatedAt,
		}
		if it.Kind == KindImage && it.ImagePath != "" {
			if rel, ok := usedImageNames[it.ImagePath]; ok {
				bi.ImageFile = rel
			} else if info, statErr := os.Stat(it.ImagePath); statErr == nil && !info.IsDir() {
				rel = uniqueArchiveImagePath(filepath.Base(it.ImagePath), usedImageNames)
				usedImageNames[it.ImagePath] = rel
				imageFiles = append(imageFiles, struct {
					srcPath string
					relPath string
				}{srcPath: it.ImagePath, relPath: rel})
				bi.ImageFile = rel
			}
		}
		backupItems = append(backupItems, bi)
	}

	manifest := BackupManifest{
		FormatVersion: backupFormatVersion,
		ExportedAt:    time.Now().UTC(),
		Encrypted:     password != "",
		ItemCount:     len(backupItems),
		ImageCount:    len(imageFiles),
	}

	if err := os.MkdirAll(filepath.Dir(zipPath), 0o755); err != nil {
		return ExportResult{}, err
	}
	tmpPath := zipPath + ".tmp"
	f, err := os.OpenFile(tmpPath, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o600)
	if err != nil {
		return ExportResult{}, err
	}
	cleanupTmp := true
	defer func() {
		_ = f.Close()
		if cleanupTmp {
			_ = os.Remove(tmpPath)
		}
	}()

	zw := zip.NewWriter(f)
	if manifest.Encrypted {
		var innerBuf bytes.Buffer
		innerZW := zip.NewWriter(&innerBuf)
		if err := writeBackupPayloadToZip(innerZW, backupItems, imageFiles); err != nil {
			_ = innerZW.Close()
			return ExportResult{}, err
		}
		if err := innerZW.Close(); err != nil {
			return ExportResult{}, err
		}

		salt := make([]byte, 16)
		if _, err := rand.Read(salt); err != nil {
			return ExportResult{}, err
		}
		nonce := make([]byte, 12)
		if _, err := rand.Read(nonce); err != nil {
			return ExportResult{}, err
		}
		key, err := pbkdf2.Key(sha256.New, password, salt, backupPBKDF2Iters, 32)
		if err != nil {
			return ExportResult{}, err
		}
		block, err := aes.NewCipher(key)
		if err != nil {
			return ExportResult{}, err
		}
		gcm, err := cipher.NewGCM(block)
		if err != nil {
			return ExportResult{}, err
		}
		sealed := gcm.Seal(nil, nonce, innerBuf.Bytes(), []byte(backupPayloadAAD))

		manifest.Salt = base64.StdEncoding.EncodeToString(salt)
		manifest.Nonce = base64.StdEncoding.EncodeToString(nonce)
		if err := writeJSONToZip(zw, backupManifestName, manifest); err != nil {
			return ExportResult{}, err
		}
		pw, err := zw.CreateHeader(&zip.FileHeader{
			Name:   backupPayloadName,
			Method: zip.Store,
		})
		if err != nil {
			return ExportResult{}, err
		}
		if _, err := pw.Write(sealed); err != nil {
			return ExportResult{}, err
		}
	} else {
		if err := writeJSONToZip(zw, backupManifestName, manifest); err != nil {
			return ExportResult{}, err
		}
		if err := writeBackupPayloadToZip(zw, backupItems, imageFiles); err != nil {
			return ExportResult{}, err
		}
	}

	if err := zw.Close(); err != nil {
		return ExportResult{}, err
	}
	if err := f.Close(); err != nil {
		return ExportResult{}, err
	}
	if err := os.Rename(tmpPath, zipPath); err != nil {
		return ExportResult{}, err
	}
	cleanupTmp = false
	return ExportResult{
		ItemCount:  manifest.ItemCount,
		ImageCount: manifest.ImageCount,
		Encrypted:  manifest.Encrypted,
	}, nil
}

// ImportBackupZip imports items and images from a backup zip archive, deduplicating by (kind, hash).
func (s *Store) ImportBackupZip(zipPath, password string) (ImportResult, error) {
	if s == nil || s.db == nil {
		return ImportResult{}, errors.New("store is not initialized")
	}
	outerZR, err := zip.OpenReader(zipPath)
	if err != nil {
		return ImportResult{}, fmt.Errorf("%w: %v", ErrBackupInvalidFormat, err)
	}
	defer outerZR.Close()

	manifest, err := readManifestFromZip(&outerZR.Reader)
	if err != nil {
		return ImportResult{}, err
	}

	var payloadZR *zip.Reader
	if manifest.Encrypted {
		if password == "" {
			return ImportResult{}, ErrBackupPasswordRequired
		}
		salt, err := base64.StdEncoding.DecodeString(manifest.Salt)
		if err != nil || len(salt) == 0 {
			return ImportResult{}, ErrBackupInvalidFormat
		}
		nonce, err := base64.StdEncoding.DecodeString(manifest.Nonce)
		if err != nil || len(nonce) != 12 {
			return ImportResult{}, ErrBackupInvalidFormat
		}
		sealed, err := readZipEntryBytes(&outerZR.Reader, backupPayloadName, backupMaxPayloadSize)
		if err != nil {
			return ImportResult{}, fmt.Errorf("%w: %v", ErrBackupInvalidFormat, err)
		}
		key, err := pbkdf2.Key(sha256.New, password, salt, backupPBKDF2Iters, 32)
		if err != nil {
			return ImportResult{}, err
		}
		block, err := aes.NewCipher(key)
		if err != nil {
			return ImportResult{}, err
		}
		gcm, err := cipher.NewGCM(block)
		if err != nil {
			return ImportResult{}, err
		}
		plainZip, err := gcm.Open(nil, nonce, sealed, []byte(backupPayloadAAD))
		if err != nil {
			return ImportResult{}, ErrBackupInvalidPassword
		}
		innerZR, err := zip.NewReader(bytes.NewReader(plainZip), int64(len(plainZip)))
		if err != nil {
			return ImportResult{}, fmt.Errorf("%w: %v", ErrBackupInvalidFormat, err)
		}
		payloadZR = innerZR
	} else {
		payloadZR = &outerZR.Reader
	}

	itemsRaw, err := readZipEntryBytes(payloadZR, backupItemsName, backupMaxItemsJSON)
	if err != nil {
		return ImportResult{}, fmt.Errorf("%w: %v", ErrBackupInvalidFormat, err)
	}
	var items []BackupItem
	if err := json.Unmarshal(itemsRaw, &items); err != nil {
		return ImportResult{}, fmt.Errorf("%w: %v", ErrBackupInvalidFormat, err)
	}

	imageEntries := make(map[string]*zip.File, len(payloadZR.File))
	for _, zf := range payloadZR.File {
		if strings.HasPrefix(zf.Name, backupImagesDir+"/") && !zf.FileInfo().IsDir() {
			imageEntries[zf.Name] = zf
		}
	}

	return s.applyImportedBackup(items, imageEntries)
}

func (s *Store) applyImportedBackup(items []BackupItem, imageEntries map[string]*zip.File) (ImportResult, error) {
	s.writeMu.Lock()
	defer s.writeMu.Unlock()

	if err := os.MkdirAll(s.imageDir, 0o755); err != nil {
		return ImportResult{}, err
	}

	existing := make(map[string]struct{})
	rows, err := s.db.Query(`SELECT kind, content_hash FROM clip_items`)
	if err != nil {
		return ImportResult{}, err
	}
	for rows.Next() {
		var kind, hash string
		if err := rows.Scan(&kind, &hash); err == nil {
			existing[kind+"\x00"+hash] = struct{}{}
		}
	}
	_ = rows.Close()

	tx, err := s.db.Begin()
	if err != nil {
		return ImportResult{}, err
	}
	var createdImages []string
	rollback := true
	defer func() {
		if rollback {
			_ = tx.Rollback()
			for _, p := range createdImages {
				_ = os.Remove(p)
			}
		}
	}()

	var res ImportResult
	var tagsChanged bool
	restoredImages := make(map[string]string)

	for _, bi := range items {
		if bi.Kind == "" || bi.Hash == "" {
			res.Skipped++
			continue
		}
		key := string(bi.Kind) + "\x00" + bi.Hash
		if _, dup := existing[key]; dup {
			res.Skipped++
			continue
		}
		existing[key] = struct{}{}

		imagePath := ""
		if bi.Kind == KindImage && bi.ImageFile != "" {
			if dst, ok := restoredImages[bi.ImageFile]; ok {
				imagePath = dst
			} else if zf, ok := imageEntries[bi.ImageFile]; ok {
				dst, created, err := restoreZipImage(zf, s.imageDir)
				if err != nil {
					return ImportResult{}, err
				}
				imagePath = dst
				restoredImages[bi.ImageFile] = dst
				if created {
					createdImages = append(createdImages, dst)
					res.Images++
				}
			}
		}

		cleanTags := normalizeTags(bi.Tags)
		if len(cleanTags) > 0 {
			tagsChanged = true
		}
		tagsJSON := "[]"
		if len(cleanTags) > 0 {
			if b, err := json.Marshal(cleanTags); err == nil {
				tagsJSON = string(b)
			}
		}
		filesJSON := "[]"
		if len(bi.Files) > 0 {
			if b, err := json.Marshal(bi.Files); err == nil {
				filesJSON = string(b)
			}
		}

		storeText, storeHTML, storePreview, err := encryptSensitiveFields(bi.Text, bi.HTML, bi.Preview, HasSensitiveTag(cleanTags))
		if err != nil {
			return ImportResult{}, err
		}

		createdMs := bi.CreatedAt.UnixMilli()
		updatedMs := bi.UpdatedAt.UnixMilli()
		if createdMs <= 0 {
			createdMs = time.Now().UnixMilli()
		}
		if updatedMs <= 0 {
			updatedMs = createdMs
		}
		pinnedVal := 0
		if bi.Pinned {
			pinnedVal = 1
		}

		if _, err := tx.Exec(`
INSERT INTO clip_items (kind, content_hash, text_content, html_content, image_path, files_json, preview, source_app, source_path, byte_size, pinned, tags_json, created_at, updated_at)
VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
`, string(bi.Kind), bi.Hash, storeText, storeHTML, imagePath, filesJSON, storePreview, bi.SourceApp, bi.SourcePath, bi.ByteSize, pinnedVal, tagsJSON, createdMs, updatedMs); err != nil {
			return ImportResult{}, err
		}
		res.Imported++
	}

	if err := tx.Commit(); err != nil {
		return ImportResult{}, err
	}
	rollback = false
	if res.Imported > 0 {
		s.bumpRevision(tagsChanged)
	}
	return res, nil
}

func (s *Store) loadAllItemsForBackup() ([]ClipItem, error) {
	rows, err := s.db.Query(`
SELECT id, kind, content_hash, text_content, html_content, image_path, files_json, preview, source_app, source_path, byte_size, pinned, tags_json, created_at, updated_at
FROM clip_items
ORDER BY updated_at ASC, id ASC
`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var out []ClipItem
	for rows.Next() {
		it, err := scanItem(rows)
		if err != nil {
			return nil, err
		}
		if err := decryptItemFields(&it); err != nil {
			return nil, err
		}
		out = append(out, it)
	}
	return out, rows.Err()
}

func writeBackupPayloadToZip(zw *zip.Writer, items []BackupItem, imageFiles []struct {
	srcPath string
	relPath string
}) error {
	if err := writeJSONToZip(zw, backupItemsName, items); err != nil {
		return err
	}
	for _, img := range imageFiles {
		src, err := os.Open(img.srcPath)
		if err != nil {
			continue
		}
		w, err := zw.CreateHeader(&zip.FileHeader{
			Name:   img.relPath,
			Method: zip.Store,
		})
		if err != nil {
			_ = src.Close()
			return err
		}
		_, copyErr := io.Copy(w, src)
		_ = src.Close()
		if copyErr != nil {
			return copyErr
		}
	}
	return nil
}

func writeJSONToZip(zw *zip.Writer, name string, v any) error {
	b, err := json.Marshal(v)
	if err != nil {
		return err
	}
	w, err := zw.Create(name)
	if err != nil {
		return err
	}
	_, err = w.Write(b)
	return err
}

func readManifestFromZip(zr *zip.Reader) (BackupManifest, error) {
	b, err := readZipEntryBytes(zr, backupManifestName, backupMaxManifest)
	if err != nil {
		return BackupManifest{}, fmt.Errorf("%w: %v", ErrBackupInvalidFormat, err)
	}
	var m BackupManifest
	if err := json.Unmarshal(b, &m); err != nil {
		return BackupManifest{}, fmt.Errorf("%w: %v", ErrBackupInvalidFormat, err)
	}
	if m.FormatVersion != backupFormatVersion {
		return BackupManifest{}, fmt.Errorf("%w: unsupported version %d", ErrBackupInvalidFormat, m.FormatVersion)
	}
	return m, nil
}

func readZipEntryBytes(zr *zip.Reader, name string, maxBytes int64) ([]byte, error) {
	for _, f := range zr.File {
		if f.Name != name {
			continue
		}
		rc, err := f.Open()
		if err != nil {
			return nil, err
		}
		defer rc.Close()
		b, err := io.ReadAll(io.LimitReader(rc, maxBytes+1))
		if err != nil {
			return nil, err
		}
		if int64(len(b)) > maxBytes {
			return nil, fmt.Errorf("entry %s exceeds size limit", name)
		}
		return b, nil
	}
	return nil, fmt.Errorf("missing %s", name)
}

func uniqueArchiveImagePath(baseName string, used map[string]string) string {
	clean := filepath.Base(baseName)
	if clean == "" || clean == "." || clean == ".." {
		clean = "image.png"
	}
	inUse := func(rel string) bool {
		for _, v := range used {
			if v == rel {
				return true
			}
		}
		return false
	}
	rel := path.Join(backupImagesDir, clean)
	if !inUse(rel) {
		return rel
	}
	ext := path.Ext(clean)
	stem := strings.TrimSuffix(clean, ext)
	for i := 1; ; i++ {
		cand := path.Join(backupImagesDir, fmt.Sprintf("%s_%d%s", stem, i, ext))
		if !inUse(cand) {
			return cand
		}
	}
}

func restoreZipImage(zf *zip.File, imageDir string) (string, bool, error) {
	base := filepath.Base(zf.Name)
	if base == "" || base == "." || base == ".." || strings.ContainsAny(base, `/\`) {
		return "", false, ErrBackupInvalidFormat
	}
	rc, err := zf.Open()
	if err != nil {
		return "", false, err
	}
	data, err := io.ReadAll(io.LimitReader(rc, backupMaxImageBytes+1))
	_ = rc.Close()
	if err != nil {
		return "", false, err
	}
	if int64(len(data)) > backupMaxImageBytes {
		return "", false, fmt.Errorf("%w: image too large", ErrBackupInvalidFormat)
	}

	ext := filepath.Ext(base)
	stem := strings.TrimSuffix(base, ext)
	for i := 0; ; i++ {
		name := base
		if i > 0 {
			name = fmt.Sprintf("%s_%d%s", stem, i, ext)
		}
		dst := filepath.Join(imageDir, name)
		if existingData, readErr := os.ReadFile(dst); readErr == nil {
			if bytes.Equal(existingData, data) {
				return dst, false, nil
			}
			continue
		}
		if err := os.WriteFile(dst, data, 0o600); err != nil {
			return "", false, err
		}
		return dst, true, nil
	}
}
