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

// Bundled returns the skills built into codebot, their references relative
// to baseDir.
func Bundled(baseDir string) []Spec {
	files, _ := fs.Glob(bundledFS, "bundled/*.md")
	specs := make([]Spec, 0, len(files))
	for _, file := range files {
		data, _ := bundledFS.ReadFile(file)
		spec, err := parseSkill(string(data), strings.TrimSuffix(path.Base(file), ".md"))
		if err != nil {
			panic(fmt.Sprintf("bundled skill %s: %v", file, err))
		}
		spec.BaseDir, spec.Source, spec.text = baseDir, "bundled", string(data)
		specs = append(specs, spec)
	}
	return specs
}
