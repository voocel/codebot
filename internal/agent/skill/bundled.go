package skill

import (
	"embed"
	"fmt"
	"io/fs"
	"path"
	"strings"
)

//go:embed bundled/*.md
var bundledFS embed.FS

func Bundled(baseDir string) []Spec {
	files, _ := fs.Glob(bundledFS, "bundled/*.md")
	specs := make([]Spec, 0, len(files))
	for _, file := range files {
		data, _ := bundledFS.ReadFile(file)
		spec, err := parseSkill(string(data), strings.TrimSuffix(path.Base(file), ".md"))
		if err != nil {
			panic(fmt.Sprintf("bundled skill %s: %v", file, err))
		}
		spec.BaseDir, spec.Privileged, spec.text, spec.frozen = baseDir, true, string(data), true
		specs = append(specs, spec)
	}
	return specs
}
