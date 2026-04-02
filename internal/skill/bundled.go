package skill

import (
	"embed"
	"io/fs"
	"path"
	"sort"
	"strings"
)

//go:embed bundled/*.md
var bundledFS embed.FS

func BundledSpecs(cwd string) []Spec {
	entries, err := fs.ReadDir(bundledFS, "bundled")
	if err != nil {
		return nil
	}

	specs := make([]Spec, 0, len(entries))
	for _, entry := range entries {
		if entry.IsDir() || !strings.HasSuffix(strings.ToLower(entry.Name()), ".md") {
			continue
		}

		virtualPath := path.Join("bundled", entry.Name())
		data, err := bundledFS.ReadFile(virtualPath)
		if err != nil {
			continue
		}

		spec, err := parseSkillContent(string(data), skillSource{
			NameHint: strings.TrimSuffix(entry.Name(), path.Ext(entry.Name())),
			FilePath: virtualPath,
			BaseDir:  cwd,
			Source:   "bundled",
		}, buildStaticPromptFn)
		if err != nil {
			continue
		}
		specs = append(specs, spec)
	}

	sort.Slice(specs, func(i, j int) bool { return specs[i].Name < specs[j].Name })
	return specs
}
