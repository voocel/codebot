package plugin

import (
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
)

type InstallInput struct {
	Cwd        string
	SourcePath string
	Scope      string
}

type InstallResult struct {
	ID           string
	Scope        string
	RootDir      string
	ManifestPath string
}

func InstallLocal(input InstallInput) (*InstallResult, error) {
	sourceRoot, manifest, err := loadInstallSource(input.SourcePath)
	if err != nil {
		return nil, err
	}

	scope := strings.ToLower(strings.TrimSpace(input.Scope))
	if scope == "" {
		scope = ScopeProject
	}
	destRoot, err := scaffoldRootDir(input.Cwd, scope, manifest.ID)
	if err != nil {
		return nil, err
	}
	if _, err := os.Stat(destRoot); err == nil {
		return nil, fmt.Errorf("plugin %q already exists at %s", manifest.ID, destRoot)
	} else if !os.IsNotExist(err) {
		return nil, fmt.Errorf("stat plugin dir %s: %w", destRoot, err)
	}

	if err := copyTree(sourceRoot, destRoot); err != nil {
		return nil, err
	}
	if err := validateManifest(filepath.Join(destRoot, "plugin.json"), destRoot, manifest); err != nil {
		return nil, err
	}

	return &InstallResult{
		ID:           manifest.ID,
		Scope:        scope,
		RootDir:      destRoot,
		ManifestPath: filepath.Join(destRoot, "plugin.json"),
	}, nil
}

func Remove(cwd string, loaded Loaded) error {
	if loaded.Scope != ScopeProject && loaded.Scope != ScopeUser {
		return fmt.Errorf("plugin scope %q cannot be removed", loaded.Scope)
	}
	if strings.TrimSpace(loaded.RootDir) == "" {
		return fmt.Errorf("plugin %q has no root dir", loaded.Manifest.ID)
	}
	if err := os.RemoveAll(loaded.RootDir); err != nil {
		return fmt.Errorf("remove plugin dir %s: %w", loaded.RootDir, err)
	}
	if err := deleteStateEntry(cwd, loaded.Scope, loaded.Manifest.ID); err != nil {
		return err
	}
	return nil
}

func loadInstallSource(sourcePath string) (string, Manifest, error) {
	path := filepath.Clean(strings.TrimSpace(sourcePath))
	if path == "" {
		return "", Manifest{}, fmt.Errorf("plugin source path is required")
	}

	info, err := os.Stat(path)
	if err != nil {
		return "", Manifest{}, fmt.Errorf("stat plugin source %s: %w", path, err)
	}
	if !info.IsDir() {
		return "", Manifest{}, fmt.Errorf("plugin source %s is not a directory", path)
	}

	manifestPath := filepath.Join(path, "plugin.json")
	data, err := os.ReadFile(manifestPath)
	if err != nil {
		return "", Manifest{}, fmt.Errorf("read plugin manifest %s: %w", manifestPath, err)
	}
	var manifest Manifest
	if err := json.Unmarshal(data, &manifest); err != nil {
		return "", Manifest{}, fmt.Errorf("parse plugin manifest %s: %w", manifestPath, err)
	}
	if err := validateManifest(manifestPath, path, manifest); err != nil {
		return "", Manifest{}, err
	}
	return path, manifest, nil
}

func copyTree(srcRoot, dstRoot string) error {
	return filepath.Walk(srcRoot, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		rel, err := filepath.Rel(srcRoot, path)
		if err != nil {
			return fmt.Errorf("resolve relative path for %s: %w", path, err)
		}
		target := filepath.Join(dstRoot, rel)
		if info.IsDir() {
			return os.MkdirAll(target, info.Mode().Perm())
		}
		return copyFile(path, target, info.Mode().Perm())
	})
}

func copyFile(src, dst string, mode os.FileMode) error {
	if err := os.MkdirAll(filepath.Dir(dst), 0o755); err != nil {
		return fmt.Errorf("mkdir parent dir for %s: %w", dst, err)
	}
	in, err := os.Open(src)
	if err != nil {
		return fmt.Errorf("open source file %s: %w", src, err)
	}
	defer in.Close()

	out, err := os.OpenFile(dst, os.O_CREATE|os.O_EXCL|os.O_WRONLY, mode)
	if err != nil {
		return fmt.Errorf("create target file %s: %w", dst, err)
	}
	defer out.Close()

	if _, err := io.Copy(out, in); err != nil {
		return fmt.Errorf("copy %s to %s: %w", src, dst, err)
	}
	return nil
}

func deleteStateEntry(cwd, scope, id string) error {
	path := statePathForScope(cwd, scope)
	if path == "" {
		return nil
	}
	current, err := loadStateFile(path)
	if err != nil {
		return err
	}
	if len(current) == 0 {
		return nil
	}
	delete(current, id)
	return writeStateFile(path, current)
}
