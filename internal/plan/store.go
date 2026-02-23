package plan

import (
	"crypto/rand"
	"fmt"
	"math/big"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

// Store manages plan markdown files in a single directory.
type Store struct {
	dir string
}

// NewStore creates a Store and ensures the directory exists.
func NewStore(dir string) *Store {
	_ = os.MkdirAll(dir, 0o755)
	return &Store{dir: dir}
}

// PlanFile represents a plan file entry for listing.
type PlanFile struct {
	Name    string // filename without .md
	ModTime int64  // unix timestamp
}

// Save writes plan content as a markdown file.
func (s *Store) Save(name, content string) error {
	final := s.path(name)
	tmp := final + ".tmp"
	if err := os.WriteFile(tmp, []byte(content), 0o600); err != nil {
		return fmt.Errorf("write plan: %w", err)
	}
	if err := os.Rename(tmp, final); err != nil {
		os.Remove(tmp)
		return fmt.Errorf("rename plan: %w", err)
	}
	return nil
}

// Load reads a plan file by name. Returns "", nil if not found.
func (s *Store) Load(name string) (string, error) {
	data, err := os.ReadFile(s.path(name))
	if os.IsNotExist(err) {
		return "", nil
	}
	if err != nil {
		return "", fmt.Errorf("read plan %s: %w", name, err)
	}
	return string(data), nil
}

// Delete removes a plan file.
func (s *Store) Delete(name string) error {
	err := os.Remove(s.path(name))
	if os.IsNotExist(err) {
		return nil
	}
	return err
}

// List returns all plan files sorted by modification time (newest first).
func (s *Store) List() ([]PlanFile, error) {
	entries, err := os.ReadDir(s.dir)
	if os.IsNotExist(err) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("read plans dir: %w", err)
	}

	var plans []PlanFile
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".md") {
			continue
		}
		info, err := e.Info()
		if err != nil {
			continue
		}
		plans = append(plans, PlanFile{
			Name:    strings.TrimSuffix(e.Name(), ".md"),
			ModTime: info.ModTime().Unix(),
		})
	}

	sort.Slice(plans, func(i, j int) bool {
		return plans[i].ModTime > plans[j].ModTime
	})
	return plans, nil
}

func (s *Store) path(name string) string {
	return filepath.Join(s.dir, name+".md")
}

// GenerateName returns a random name in adjective-gerund-noun format.
func GenerateName() string {
	return pick(adjectives) + "-" + pick(gerunds) + "-" + pick(nouns)
}

func pick(list []string) string {
	n, _ := rand.Int(rand.Reader, big.NewInt(int64(len(list))))
	return list[n.Int64()]
}

var adjectives = []string{
	"agile", "bold", "bright", "calm", "clever", "cozy", "crisp",
	"dainty", "eager", "fancy", "gentle", "glowing", "happy", "humble",
	"jolly", "keen", "lively", "mellow", "noble", "plucky", "proud",
	"quiet", "quirky", "rapid", "silly", "smooth", "snazzy", "sorted",
	"swift", "tidy", "vivid", "warm", "witty", "zesty",
}

var gerunds = []string{
	"baking", "bouncing", "crafting", "dancing", "drawing", "flying",
	"frolicking", "gathering", "giggling", "gliding", "hiking", "hugging",
	"humming", "imagining", "jogging", "juggling", "jumping", "knitting",
	"laughing", "mapping", "napping", "painting", "peeking", "reading",
	"sailing", "singing", "snuggling", "sprouting", "squishing", "swimming",
	"thinking", "typing", "walking", "wobbling", "writing", "yawning",
}

var nouns = []string{
	"breeze", "candle", "cascade", "crown", "dawn", "duckling", "eagle",
	"feather", "flame", "fountain", "garden", "grove", "harbor", "island",
	"journal", "lantern", "maple", "meadow", "narwhal", "ocean", "orbit",
	"pebble", "quilt", "rainbow", "river", "rose", "spark", "star",
	"sunset", "tulip", "valley", "whale",
}
