package imageinput

import (
	"encoding/base64"
	"fmt"
	"os/exec"
	"runtime"
	"slices"
	"strings"
)

// ReadImage attempts to read image data from the system clipboard.
// Returns PNG-encoded bytes on success.
// Returns (nil, nil) when the clipboard contains no image.
func ReadImage() ([]byte, error) {
	switch runtime.GOOS {
	case "darwin":
		return readImageDarwin()
	case "linux":
		return readImageLinux()
	default:
		return nil, fmt.Errorf("clipboard image reading not supported on %s", runtime.GOOS)
	}
}

// readImageDarwin reads clipboard image on macOS via osascript with JXA.
// Tries PNG first; falls back to NSImage (handles TIFF, JPEG, etc.) converted to PNG.
func readImageDarwin() ([]byte, error) {
	cmd := exec.Command("osascript", "-l", "JavaScript", "-e", `
ObjC.import('AppKit');
var pb = $.NSPasteboard.generalPasteboard;
var pngData = pb.dataForType($.NSPasteboardTypePNG);
if (!pngData.isNil()) {
    pngData.base64EncodedStringWithOptions(0).js;
} else {
    var img = $.NSImage.alloc.initWithPasteboard(pb);
    if (img.isNil()) {
        '';
    } else {
        var tiff = img.TIFFRepresentation;
        var rep = $.NSBitmapImageRep.imageRepWithData(tiff);
        var png = rep.representationUsingTypeProperties($.NSBitmapImageFileTypePNG, $({}));
        if (png.isNil()) {
            '';
        } else {
            png.base64EncodedStringWithOptions(0).js;
        }
    }
}`)

	out, err := cmd.Output()
	if err != nil {
		return nil, nil // osascript failed — no clipboard access
	}
	b64 := strings.TrimSpace(string(out))
	if b64 == "" {
		return nil, nil
	}
	data, err := base64.StdEncoding.DecodeString(b64)
	if err != nil {
		return nil, fmt.Errorf("decode clipboard image: %w", err)
	}
	return data, nil
}

// readImageLinux reads clipboard image on Linux.
// Tries wl-paste (Wayland), xclip, xsel in order.
func readImageLinux() ([]byte, error) {
	// Wayland: wl-paste
	if path, _ := exec.LookPath("wl-paste"); path != "" {
		data, err := exec.Command("wl-paste", "-t", "image/png").Output()
		if err == nil && len(data) > 0 {
			return data, nil
		}
	}

	// X11: xclip
	if path, _ := exec.LookPath("xclip"); path != "" {
		return readWithXclip()
	}

	// X11: xsel
	if path, _ := exec.LookPath("xsel"); path != "" {
		data, err := exec.Command("xsel", "--clipboard", "--output").Output()
		if err == nil && len(data) > 0 && isPNG(data) {
			return data, nil
		}
		return nil, nil
	}

	return nil, nil
}

// readWithXclip reads PNG image from clipboard via xclip.
func readWithXclip() ([]byte, error) {
	targets := exec.Command("xclip", "-selection", "clipboard", "-t", "TARGETS", "-o")
	out, err := targets.Output()
	if err != nil {
		return nil, nil
	}

	if !slices.Contains(splitLines(out), "image/png") {
		return nil, nil
	}

	data, err := exec.Command("xclip", "-selection", "clipboard", "-t", "image/png", "-o").Output()
	if err != nil || len(data) == 0 {
		return nil, nil
	}
	return data, nil
}

// isPNG checks if data starts with PNG signature.
func isPNG(data []byte) bool {
	return len(data) >= 8 &&
		data[0] == 0x89 && data[1] == 0x50 && data[2] == 0x4E && data[3] == 0x47
}

// splitLines splits byte data into trimmed non-empty lines.
// Handles both \n and \r\n line endings.
func splitLines(data []byte) []string {
	var lines []string
	start := 0
	for i, b := range data {
		if b == '\n' {
			end := i
			if end > start && data[end-1] == '\r' {
				end--
			}
			line := string(data[start:end])
			if line != "" {
				lines = append(lines, line)
			}
			start = i + 1
		}
	}
	if start < len(data) {
		end := len(data)
		if end > start && data[end-1] == '\r' {
			end--
		}
		line := string(data[start:end])
		if line != "" {
			lines = append(lines, line)
		}
	}
	return lines
}
