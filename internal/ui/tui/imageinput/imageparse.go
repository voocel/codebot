// Package imageinput turns clipboard images and dropped image files into
// image blocks.
package imageinput

import (
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"strings"

	"github.com/voocel/litellm"
)

const maxImageSize = 20 << 20

var supportedMIME = map[string]bool{
	"image/png":  true,
	"image/jpeg": true,
	"image/gif":  true,
	"image/webp": true,
}

func FromBytes(data []byte) (litellm.ImageBlock, error) {
	if int64(len(data)) > maxImageSize {
		return litellm.ImageBlock{}, fmt.Errorf("image too large (%d bytes, max %d)", len(data), maxImageSize)
	}
	mime := http.DetectContentType(data)
	if !supportedMIME[mime] {
		return litellm.ImageBlock{}, fmt.Errorf("unsupported image type: %s", mime)
	}
	return litellm.ImageBlock{Data: data, MIME: mime}, nil
}

var imageExts = map[string]bool{
	".png": true, ".jpg": true, ".jpeg": true,
	".gif": true, ".webp": true,
}

// ParseDroppedPath extracts an image path from a terminal drag-and-drop
// paste, which may be quoted, backslash-escaped or raw. It returns "" for
// anything else.
func ParseDroppedPath(text string) string {
	p := strings.TrimSpace(text)
	if p == "" || strings.ContainsAny(p, "\n\r") {
		return "" // empty or multi-file drop
	}
	if len(p) >= 2 {
		if (p[0] == '\'' && p[len(p)-1] == '\'') || (p[0] == '"' && p[len(p)-1] == '"') {
			p = p[1 : len(p)-1]
		}
	}
	// macOS Terminal escapes spaces, parens, etc.
	if strings.Contains(p, `\`) {
		var b strings.Builder
		b.Grow(len(p))
		for i := 0; i < len(p); i++ {
			if p[i] == '\\' && i+1 < len(p) {
				i++
			}
			b.WriteByte(p[i])
		}
		p = b.String()
	}
	if !imageExts[strings.ToLower(filepath.Ext(p))] {
		return ""
	}
	return p
}

func LoadFile(path string) (litellm.ImageBlock, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return litellm.ImageBlock{}, err
	}
	return FromBytes(data)
}
