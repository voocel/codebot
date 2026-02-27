package imageinput

import (
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
