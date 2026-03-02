package imageinput

import (
	"os"
	"path/filepath"
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
	block, err := FromBytes(minimalPNG)
	if err != nil {
		t.Fatal(err)
	}
	if block.Image == nil {
		t.Fatal("expected image block")
	}
	if block.Image.MimeType != "image/png" {
		t.Errorf("mime = %q, want image/png", block.Image.MimeType)
	}
	if block.Image.Data == "" {
		t.Error("expected non-empty base64 data")
	}
}

func TestFromBytes_UnsupportedMIME(t *testing.T) {
	_, err := FromBytes([]byte("this is not an image"))
	if err == nil {
		t.Fatal("expected error for unsupported MIME type")
	}
}

func TestFromBytes_TooLarge(t *testing.T) {
	data := make([]byte, maxImageSize+1)
	copy(data, minimalPNG) // valid header but oversized
	_, err := FromBytes(data)
	if err == nil {
		t.Fatal("expected error for oversized image")
	}
}

func TestFromBytes_Empty(t *testing.T) {
	_, err := FromBytes(nil)
	if err == nil {
		t.Fatal("expected error for empty data")
	}
}

func TestParseDroppedPath(t *testing.T) {
	tests := []struct {
		name string
		in   string
		want string
	}{
		{"raw png", "/tmp/test.png", "/tmp/test.png"},
		{"raw jpg", "/home/user/photo.jpg", "/home/user/photo.jpg"},
		{"raw jpeg", "/tmp/shot.JPEG", "/tmp/shot.JPEG"},
		{"single quoted", "'/tmp/my file.png'", "/tmp/my file.png"},
		{"double quoted", `"/tmp/my file.webp"`, "/tmp/my file.webp"},
		{"escaped spaces", `/tmp/my\ file.png`, "/tmp/my file.png"},
		{"escaped parens", `/tmp/photo\ \(1\).png`, "/tmp/photo (1).png"},
		{"escaped ampersand", `/tmp/A\ \&\ B.jpg`, "/tmp/A & B.jpg"},
		{"whitespace padding", "  /tmp/test.gif  ", "/tmp/test.gif"},
		{"multi-file newline", "/tmp/a.png\n/tmp/b.png", ""},
		{"multi-file crlf", "/tmp/a.png\r\n/tmp/b.png", ""},
		{"non-image txt", "/tmp/readme.txt", ""},
		{"non-image go", "/tmp/main.go", ""},
		{"empty", "", ""},
		{"just spaces", "   ", ""},
		{"no extension", "/tmp/noext", ""},
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

func TestLoadFile(t *testing.T) {
	tmp := filepath.Join(t.TempDir(), "test.png")
	if err := os.WriteFile(tmp, minimalPNG, 0o644); err != nil {
		t.Fatal(err)
	}
	block, err := LoadFile(tmp)
	if err != nil {
		t.Fatal(err)
	}
	if block.Image == nil {
		t.Fatal("expected image block")
	}
	if block.Image.MimeType != "image/png" {
		t.Errorf("mime = %q, want image/png", block.Image.MimeType)
	}
}
