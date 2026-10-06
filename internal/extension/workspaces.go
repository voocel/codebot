package extension

import (
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"

	"github.com/voocel/codebot/internal/infra/config"
)

// Workspace is what the user decided about a project, kept in their home,
// where no project can decide for them.
type Workspace struct {
	// Trust is their decision on the project's surface, nil until they make
	// one.
	Trust *Decision `json:"trust,omitempty"`
	// Disabled are the plugins they turned off there, "plugin:<name>".
	Disabled []string `json:"disabled,omitempty"`
}

// Decision is the user's decision on a project's surface: to trust it or
// not, and the surface they decided on.
type Decision struct {
	Trusted bool    `json:"trusted"`
	Surface Surface `json:"surface,omitempty"`
}

// workspacesPath is ~/.codebot/workspaces.json: each project's Workspace,
// by root.
func workspacesPath() string { return filepath.Join(config.UserConfigDir(), "workspaces.json") }

// ReadWorkspace returns what the user decided about the project at root.
func ReadWorkspace(root string) (Workspace, error) {
	all, err := readWorkspaces()
	return all[root], err
}

// EditWorkspace applies edit to what the user decided about the project at
// root.
func EditWorkspace(root string, edit func(*Workspace)) error {
	if err := os.MkdirAll(config.UserConfigDir(), 0o755); err != nil {
		return err
	}
	unlock, err := config.LockFile(workspacesPath())
	if err != nil {
		return err
	}
	defer unlock()
	all, err := readWorkspaces()
	if err != nil {
		return err
	}
	w := all[root]
	edit(&w)
	all[root] = w
	data, err := json.MarshalIndent(all, "", "  ")
	if err != nil {
		return err
	}
	return config.WriteFileAtomic(workspacesPath(), data, 0o600)
}

func readWorkspaces() (map[string]Workspace, error) {
	all := map[string]Workspace{}
	data, err := os.ReadFile(workspacesPath())
	if errors.Is(err, fs.ErrNotExist) {
		return all, nil
	}
	if err != nil {
		return nil, err
	}
	if err := json.Unmarshal(data, &all); err != nil {
		return nil, fmt.Errorf("%s: %w", workspacesPath(), err)
	}
	return all, nil
}
