package imageinput

import (
	"bytes"
	"testing"
)

// minimalPNG is a valid 1x1 PNG for testing.
var minimalPNG = []byte{
	0x89, 0x50, 0x4E, 0x47, 0x0D, 0x0A, 0x1A, 0x0A, // PNG signature
	0x00, 0x00, 0x00, 0x0D, // IHDR length
	0x49, 0x48, 0x44, 0x52, // "IHDR"
	0x00, 0x00, 0x00, 0x01, // width: 1
	0x00, 0x00, 0x00, 0x01, // height: 1
	0x08, 0x02, // bit depth: 8, color type: 2 (RGB)
	0x00, 0x00, 0x00, // compression, filter, interlace
	0x90, 0x77, 0x53, 0xDE, // CRC
	0x00, 0x00, 0x00, 0x00, // IEND length
	0x49, 0x45, 0x4E, 0x44, // "IEND"
	0xAE, 0x42, 0x60, 0x82, // CRC
}

func TestFromBytes(t *testing.T) {
	tooLarge := make([]byte, maxImageSize+1)
	copy(tooLarge, minimalPNG) // valid header but oversized
	tests := []struct {
		name string
		data []byte
		want string // the MIME type, "" for refused
	}{
		{"png", minimalPNG, "image/png"},
		{"unsupported MIME", []byte("this is not an image"), ""},
		{"too large", tooLarge, ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			block, err := FromBytes(tt.data)
			if block.MIME != tt.want || (err == nil) != (tt.want != "") {
				t.Fatalf("block = %q, err = %v; want %q", block.MIME, err, tt.want)
			}
			if tt.want != "" && !bytes.Equal(block.Data, tt.data) {
				t.Errorf("block holds %d bytes, want %d", len(block.Data), len(tt.data))
			}
		})
	}
}

func TestParseDroppedPath(t *testing.T) {
	tests := []struct {
		name string
		in   string
		want string
	}{
		{"raw png", "/tmp/test.png", "/tmp/test.png"},
		{"raw jpeg", "/tmp/shot.JPEG", "/tmp/shot.JPEG"},
		{"single quoted", "'/tmp/my file.png'", "/tmp/my file.png"},
		{"double quoted", `"/tmp/my file.webp"`, "/tmp/my file.webp"},
		{"escaped spaces", `/tmp/my\ file.png`, "/tmp/my file.png"},
		{"escaped parens", `/tmp/photo\ \(1\).png`, "/tmp/photo (1).png"},
		{"whitespace padding", "  /tmp/test.gif  ", "/tmp/test.gif"},
		{"multi-file newline", "/tmp/a.png\n/tmp/b.png", ""},
		{"non-image txt", "/tmp/readme.txt", ""},
		{"empty", "", ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := ParseDroppedPath(tt.in)
			if got != tt.want {
				t.Errorf("ParseDroppedPath(%q) = %q, want %q", tt.in, got, tt.want)
			}
		})
	}
}
