// Package frontmatter splits the YAML frontmatter off a markdown file.
package frontmatter

import "strings"

// Split returns the YAML between a file's leading "---" line and the next
// "---" line, and the body after it. ok is false, with body the whole
// content, when the file opens no frontmatter or never closes it. A
// delimiter line may end in spaces, tabs or CR.
func Split(content string) (front, body string, ok bool) {
	first, rest, _ := strings.Cut(content, "\n")
	if !delimiter(first) {
		return "", content, false
	}
	for off := 0; off < len(rest); {
		line, _, _ := strings.Cut(rest[off:], "\n")
		if delimiter(line) {
			return rest[:off], rest[min(off+len(line)+1, len(rest)):], true
		}
		off += len(line) + 1
	}
	return "", content, false
}

func delimiter(line string) bool { return strings.TrimRight(line, " \t\r") == "---" }
