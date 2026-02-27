// Package imageinput handles image input from clipboard paste and file drag-drop,
// converting raw data to agentcore ContentBlocks.
package imageinput

import (
	"encoding/base64"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"strings"

	"github.com/voocel/agentcore"
)

// maxImageSize is the upper limit for a single image (20 MB).
const maxImageSize = 20 << 20

// supportedMIME lists MIME types accepted by the LLM.
var supportedMIME = map[string]bool{
	"image/png":  true,
	"image/jpeg": true,
	"image/gif":  true,
	"image/webp": true,
}

// FromBytes validates raw image data and returns an ImageBlock.
// Checks size limit and MIME type.
func FromBytes(data []byte) (agentcore.ContentBlock, error) {
	if int64(len(data)) > maxImageSize {
		return agentcore.ContentBlock{}, fmt.Errorf("image too large (%d bytes, max %d)", len(data), maxImageSize)
	}
	mime := http.DetectContentType(data)
	if !supportedMIME[mime] {
		return agentcore.ContentBlock{}, fmt.Errorf("unsupported image type: %s", mime)
	}
	b64 := base64.StdEncoding.EncodeToString(data)
	return agentcore.ImageBlock(b64, mime), nil
}

// imageExts lists file extensions recognized as images for drag-drop.
var imageExts = map[string]bool{
	".png": true, ".jpg": true, ".jpeg": true,
	".gif": true, ".webp": true,
}

// ParseDroppedPath extracts an image file path from bracketed-paste text.
// Handles terminal drag-drop formats: quoted, backslash-escaped, raw.
// Returns "" if the text is not an image file path.
func ParseDroppedPath(text string) string {
	p := strings.TrimSpace(text)
	if p == "" || strings.ContainsAny(p, "\n\r") {
		return "" // empty or multi-file drop
	}
	// Strip surrounding quotes (single or double).
	if len(p) >= 2 {
		if (p[0] == '\'' && p[len(p)-1] == '\'') || (p[0] == '"' && p[len(p)-1] == '"') {
			p = p[1 : len(p)-1]
		}
	}
	// Unescape backslash sequences (macOS Terminal escapes spaces, parens, etc.).
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

// LoadFile reads an image file and returns a validated ContentBlock.
func LoadFile(path string) (agentcore.ContentBlock, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return agentcore.ContentBlock{}, err
	}
	return FromBytes(data)
}
