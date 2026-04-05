package storage

import (
	"crypto/rand"
	"fmt"
	"math/big"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

// PlanStore manages plan markdown files in a single directory.
type PlanStore struct {
	dir string
}

// NewPlanStore creates a PlanStore and ensures the directory exists.
func NewPlanStore(dir string) *PlanStore {
	_ = os.MkdirAll(dir, 0o755)
	return &PlanStore{dir: dir}
}

// PlanFile represents a plan file entry for listing.
type PlanFile struct {
	Name    string // filename without .md
	ModTime int64  // unix timestamp
}

// Save writes plan content as a markdown file.
func (s *PlanStore) Save(name, content string) error {
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
func (s *PlanStore) Load(name string) (string, error) {
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
func (s *PlanStore) Delete(name string) error {
	err := os.Remove(s.path(name))
	if os.IsNotExist(err) {
		return nil
	}
	return err
}

// List returns all plan files sorted by modification time (newest first).
func (s *PlanStore) List() ([]PlanFile, error) {
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

func (s *PlanStore) path(name string) string {
	return filepath.Join(s.dir, name+".md")
}

func (s *PlanStore) Path(name string) string {
	return s.path(name)
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
	"agile", "amber", "arctic", "bold", "brave", "bright", "calm", "clever",
	"coral", "cozy", "crisp", "dainty", "dapper", "eager", "emerald", "fancy",
	"fierce", "gentle", "gleaming", "glowing", "golden", "grand", "happy",
	"humble", "ivory", "jade", "jolly", "keen", "lively", "lunar", "mellow",
	"mighty", "misty", "noble", "olive", "opal", "plucky", "polar", "proud",
	"quiet", "quirky", "rapid", "rosy", "rustic", "sandy", "scarlet", "serene",
	"sharp", "silly", "sleek", "smooth", "snazzy", "solar", "sorted", "steady",
	"sunny", "swift", "tidy", "vivid", "warm", "witty", "zesty",
}

var gerunds = []string{
	"baking", "blazing", "bouncing", "brewing", "carving", "catching",
	"crafting", "dashing", "dancing", "drifting", "drawing", "dreaming",
	"fishing", "floating", "flying", "forging", "frolicking", "gardening",
	"gathering", "giggling", "gliding", "growing", "hiking", "hugging",
	"humming", "imagining", "jogging", "juggling", "jumping", "knitting",
	"laughing", "leaping", "mapping", "marching", "napping", "painting",
	"peeking", "planting", "racing", "reading", "roaming", "rolling",
	"sailing", "singing", "skating", "sketching", "snuggling", "soaring",
	"sprouting", "squishing", "stacking", "surfing", "swimming", "swinging",
	"thinking", "tracing", "typing", "walking", "weaving", "wobbling",
	"writing", "yawning",
}

var nouns = []string{
	"aurora", "bamboo", "beacon", "birch", "blossom", "breeze", "brook",
	"candle", "canyon", "cascade", "cedar", "cliff", "cloud", "coral",
	"creek", "crown", "crystal", "dawn", "delta", "dune", "duckling",
	"eagle", "ember", "falcon", "feather", "fern", "flame", "forest",
	"fountain", "garden", "glacier", "grove", "harbor", "heron", "island",
	"journal", "lagoon", "lantern", "linden", "maple", "meadow", "narwhal",
	"ocean", "orbit", "otter", "pebble", "pine", "quilt", "rainbow",
	"reef", "ridge", "river", "robin", "rose", "spark", "spruce", "star",
	"summit", "sunset", "tulip", "valley", "whale", "willow",
}
