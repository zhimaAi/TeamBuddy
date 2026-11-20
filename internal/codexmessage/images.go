package codexmessage

import (
	"context"
	"errors"
	"io"
	"net/http"
	"os"
	"path/filepath"

	"github.com/google/uuid"
)

// ReadImage accepts only bounded regular image files, including when the source is a temporary file.
func ReadImage(source string) ([]byte, error) {
	if !filepath.IsAbs(source) {
		return nil, errors.New("image path is not absolute")
	}
	root, err := os.OpenRoot(filepath.Dir(source))
	if err != nil {
		return nil, err
	}
	defer root.Close()
	name := filepath.Base(source)
	info, err := root.Lstat(name)
	if err != nil {
		return nil, err
	}
	if !info.Mode().IsRegular() || info.Size() > MaxImageSize {
		return nil, errors.New("image is not a bounded regular file")
	}
	file, err := root.Open(name)
	if err != nil {
		return nil, err
	}
	defer file.Close()
	opened, err := file.Stat()
	if err != nil || !os.SameFile(info, opened) {
		return nil, errors.New("image changed while opening")
	}
	data, err := io.ReadAll(io.LimitReader(file, MaxImageSize+1))
	if err != nil {
		return nil, err
	}
	if len(data) == 0 || len(data) > MaxImageSize {
		return nil, errors.New("image size is invalid")
	}
	contentType := http.DetectContentType(data)
	valid := map[string]string{".png": "image/png", ".jpg": "image/jpeg", ".jpeg": "image/jpeg", ".gif": "image/gif", ".webp": "image/webp"}
	if valid[imageExtension(name)] != contentType {
		return nil, errors.New("image content does not match its extension")
	}
	return data, nil
}

// Capture preserves attachment bytes before Codex's temporary files disappear. Text delivery is independent.
func Capture(ctx context.Context, root, eventID, content string) {
	message := Parse(content, eventID)
	if message == nil {
		return
	}
	directory := filepath.Join(root, "attachments")
	if info, err := os.Lstat(directory); err == nil && !info.IsDir() {
		return
	}
	if os.MkdirAll(directory, 0700) != nil {
		return
	}
	cache, err := os.OpenRoot(directory)
	if err != nil {
		return
	}
	defer cache.Close()
	total := 0
	for _, image := range message.Attachments {
		if ctx.Err() != nil {
			return
		}
		if !image.IsImage {
			continue
		}
		name := filepath.Base(image.RelativePath)
		if info, err := cache.Lstat(name); err == nil && info.Mode().IsRegular() {
			continue
		}
		data, err := ReadImage(image.Path)
		if err != nil {
			continue
		}
		total += len(data)
		if total > MaxTotalSize {
			return
		}
		// A partial write is never published under the stable event identity.
		tempName := ".codex-image-" + uuid.NewString()
		temporary, err := cache.OpenFile(tempName, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0600)
		if err != nil {
			continue
		}
		_, err = temporary.Write(data)
		closeErr := temporary.Close()
		if err == nil && closeErr == nil {
			_ = cache.Rename(tempName, name)
		}
		_ = cache.Remove(tempName)
	}
}
