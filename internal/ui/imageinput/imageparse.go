// Package imageinput reads clipboard images and converts them to agentcore ContentBlocks.
// Image file paths in user text are handled by the model via the Read tool,
// not parsed at the TUI layer.
package imageinput

import (
	"encoding/base64"
	"fmt"
	"net/http"

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
