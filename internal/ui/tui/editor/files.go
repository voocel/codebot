package editor

import (
	"cmp"
	"io/fs"
	"maps"
	"os/exec"
	"path"
	"path/filepath"
	"slices"
	"strings"
)

const (
	maxFiles   = 50000 // files listed for "@", which bounds the walk of a huge tree
	maxMatches = 50    // files the menu offers for a query
)

type filesMsg struct{ files []string }

// listFiles returns paths relative to root: files, plus their directories
// with a trailing slash. In a git repository it skips ignored files;
// elsewhere it skips hidden ones.
func listFiles(root string) []string {
	files, ok := gitFiles(root)
	if !ok {
		files = walkFiles(root)
	}
	dirs := map[string]bool{}
	for _, f := range files {
		for d := path.Dir(f); d != "." && !dirs[d]; d = path.Dir(d) {
			dirs[d] = true
		}
	}
	out := make([]string, 0, len(dirs)+len(files))
	for _, d := range slices.Sorted(maps.Keys(dirs)) {
		out = append(out, d+"/")
	}
	return append(out, files...)
}

func gitFiles(root string) ([]string, bool) {
	cmd := exec.Command("git", "ls-files", "--cached", "--others", "--exclude-standard", "-z")
	cmd.Dir = root
	out, err := cmd.Output()
	if err != nil {
		return nil, false
	}
	files := strings.Split(strings.TrimSuffix(string(out), "\x00"), "\x00")
	if files[0] == "" {
		files = nil
	}
	if len(files) > maxFiles {
		files = files[:maxFiles]
	}
	return files, true
}

func walkFiles(root string) []string {
	var files []string
	filepath.WalkDir(root, func(p string, d fs.DirEntry, err error) error {
		switch {
		case err != nil:
			return nil
		case p != root && strings.HasPrefix(d.Name(), "."):
			if d.IsDir() {
				return fs.SkipDir
			}
			return nil
		case len(files) == maxFiles:
			return fs.SkipAll
		}
		if !d.IsDir() {
			rel, _ := filepath.Rel(root, p)
			files = append(files, filepath.ToSlash(rel))
		}
		return nil
	})
	return files
}

// rank breaks ties by the shorter path.
func rank(paths []string, q string) []string {
	type hit struct {
		path       string
		tier, span int
	}
	q = strings.ToLower(q)
	var hits []hit
	for _, p := range paths {
		if tier, span := score(strings.ToLower(p), q); tier > 0 {
			hits = append(hits, hit{p, tier, span})
		}
	}
	slices.SortStableFunc(hits, func(a, b hit) int {
		return cmp.Or(b.tier-a.tier, a.span-b.span, len(a.path)-len(b.path))
	})
	out := make([]string, 0, min(len(hits), maxMatches))
	for _, h := range hits[:min(len(hits), maxMatches)] {
		out = append(out, h.path)
	}
	return out
}

// score expects p and q in lower case. A higher tier is better and 0 means
// no match. For an in-order rune match, span is the distance from the first
// to the last matched rune.
func score(p, q string) (tier, span int) {
	name := path.Base(p)
	switch {
	case p == q && strings.HasSuffix(p, "/"):
		return 0, 0 // a directory picked offers what it holds, not itself
	case name == q:
		return 5, 0
	case strings.HasPrefix(name, q):
		return 4, 0
	case strings.Contains(name, q):
		return 3, 0
	case strings.Contains(p, q):
		return 2, 0
	}
	first, i := -1, 0
	qr := []rune(q)
	for j, r := range []rune(p) {
		if i < len(qr) && r == qr[i] {
			if i == 0 {
				first = j
			}
			if i++; i == len(qr) {
				return 1, j - first
			}
		}
	}
	return 0, 0
}
