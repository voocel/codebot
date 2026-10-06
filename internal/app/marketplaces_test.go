package app

import (
	"context"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/voocel/codebot/internal/extension"
	"github.com/voocel/codebot/internal/extension/plugin"
	"github.com/voocel/codebot/internal/infra/config"
)

// writeMarketplace writes the marketplace named name under root, listing
// what plugins holds.
func writeMarketplace(t *testing.T, root, name, plugins string) {
	t.Helper()
	file := filepath.Join(root, filepath.FromSlash(plugin.MarketplaceFile))
	if err := os.MkdirAll(filepath.Dir(file), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(file, []byte(`{"name": "`+name+`", "plugins": `+plugins+`}`), 0o644); err != nil {
		t.Fatal(err)
	}
}

// The user's and the project's own marketplaces list plugins to add by
// name; one in the project is declared relative to its settings.
func TestLocalMarketplaces(t *testing.T) {
	e := boot(t, setup{git: true, project: map[string]any{}}, map[string]*fakeModel{"claude-sonnet-4-5": script()})
	home := os.Getenv("HOME")
	writeMarketplace(t, home, "mine", `[{"name": "kit", "source": "./kit"}, {"name": "pkg", "source": {"source": "npm", "package": "@acme/pkg"}}]`)
	writeKit(t, filepath.Join(home, "kit"), false)
	writeMarketplace(t, e.cwd, "team", `[{"name": "tools", "source": {"source": "local", "path": "./plugins/tools"}}]`)
	tools := filepath.Join(e.cwd, "plugins", "tools")
	writeKit(t, tools, false)
	if err := os.WriteFile(filepath.Join(tools, "plugin.json"), []byte(`{"$schema": "https://agent-plugins.org/schemas/1.0.0/plugin.schema.json", "name": "tools"}`), 0o644); err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()

	ms, errs := e.app.Marketplaces(ctx)
	if len(errs) > 0 || len(ms) != 2 || ms[0].Name != "mine" || ms[1].Name != "team" {
		t.Fatalf("marketplaces %+v, %v", ms, errs)
	}
	if pkg := ms[0].Plugins[1]; pkg.Source != "" || pkg.Unsupported == "" {
		t.Errorf("an npm plugin listed as %+v", pkg)
	}
	if _, err := e.app.OfferPlugin(ctx, "pkg@mine", false); err == nil {
		t.Error("an npm plugin was offered")
	}
	if _, err := e.app.OfferPlugin(ctx, "kit@nowhere", false); err == nil {
		t.Error("a plugin of no marketplace was offered")
	}

	o, err := e.app.OfferPlugin(ctx, "kit@mine", false)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := e.app.AddPlugin(ctx, o); err != nil {
		t.Fatal(err)
	}
	if o, err = e.app.OfferPlugin(ctx, "tools@team", true); err != nil {
		t.Fatal(err)
	}
	if _, err := e.app.AddPlugin(ctx, o); err != nil {
		t.Fatal(err)
	}
	layers, _ := config.Load(e.cwd)
	if !slices.Equal(layers.User.Plugins, []string{filepath.Join(home, "kit")}) || !slices.Equal(layers.Project.Plugins, []string{"../plugins/tools"}) {
		t.Errorf("the user's plugins %q, the project's %q", layers.User.Plugins, layers.Project.Plugins)
	}
}

// A marketplace in a git repository is added to the user's settings; the
// plugins it lists in its own directories come from the repository.
func TestGitMarketplace(t *testing.T) {
	repo, commit := remote(t)
	writeMarketplace(t, repo, "acme", `[{"name": "kit", "source": "./plugins/kit", "description": "release tools"}]`)
	writeKit(t, filepath.Join(repo, "plugins", "kit"), false)
	commit("v1")
	e := boot(t, setup{}, map[string]*fakeModel{"claude-sonnet-4-5": script()})
	ctx := context.Background()

	m, problems, err := e.app.AddMarketplace(ctx, "example.test/acme/kit")
	if err != nil || len(problems) > 0 {
		t.Fatal(err, problems)
	}
	want := plugin.Listing{Name: "kit", Description: "release tools", Source: "https://example.test/acme/kit//plugins/kit"}
	if m.Name != "acme" || !slices.Equal(m.Plugins, []plugin.Listing{want}) {
		t.Fatalf("marketplace %+v", m)
	}
	if _, _, err := e.app.AddMarketplace(ctx, "https://example.test/acme/kit"); err == nil {
		t.Error("a marketplace was added twice, spelt otherwise")
	}
	if ms, errs := e.app.Marketplaces(ctx); len(errs) > 0 || len(ms) != 1 || ms[0].Where != "https://example.test/acme/kit" {
		t.Fatalf("marketplaces %+v, %v", ms, errs)
	}

	o, err := e.app.OfferPlugin(ctx, "kit@acme", false)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := e.app.AddPlugin(ctx, o); err != nil {
		t.Fatal(err)
	}
	if pl := e.app.Plugins()[0]; pl.Source != want.Source || pl.State != PluginOn || !slices.Contains(skillNames(e), "kit:release") {
		t.Errorf("plugin %+v, skills %q", pl, skillNames(e))
	}

	// The mirror follows the marketplace, keeping its latest commit alone.
	src, _ := plugin.ParseSource("example.test/acme/kit", "")
	before := plugin.Mirrored(src, extension.MarketplacesDir())
	writeMarketplace(t, repo, "acme", `[{"name": "kit", "source": "./plugins/kit", "description": "release tools, v2"}]`)
	commit("v2")
	if ms, errs := e.app.Marketplaces(ctx); len(errs) > 0 || ms[0].Plugins[0].Description != "release tools, v2" {
		t.Fatalf("marketplaces %+v, %v", ms, errs)
	}
	if after := plugin.Mirrored(src, extension.MarketplacesDir()); after == before {
		t.Error("the mirror stayed at the old commit")
	} else if _, err := os.Stat(before); err == nil {
		t.Error("the old commit is still mirrored")
	}

	// Gone, it is read as it was fetched last.
	if err := os.Rename(repo, repo+".gone"); err != nil {
		t.Fatal(err)
	}
	if ms, errs := e.app.Marketplaces(ctx); len(ms) != 1 || len(errs) != 1 || !strings.Contains(errs[0].Error(), "as fetched before") {
		t.Errorf("unreachable, marketplaces %+v, %v", ms, errs)
	}

	if err := e.app.RemoveMarketplace("https://example.test/acme/kit"); err != nil {
		t.Fatal(err)
	}
	if layers, _ := config.Load(e.cwd); len(layers.User.Marketplaces) > 0 {
		t.Errorf("marketplaces left %q", layers.User.Marketplaces)
	}
}

// A marketplace's name is its alone: of two of one name the first read is
// kept, and one taking a name in use is not added.
func TestMarketplaceNames(t *testing.T) {
	e := boot(t, setup{git: true, project: map[string]any{}}, map[string]*fakeModel{"claude-sonnet-4-5": script()})
	home := os.Getenv("HOME")
	writeMarketplace(t, home, "acme", `[{"name": "kit", "source": "./kit"}]`)
	writeMarketplace(t, e.cwd, "acme", `[{"name": "tools", "source": "./tools"}]`)
	other := t.TempDir()
	writeMarketplace(t, other, "acme", `[]`)
	ctx := context.Background()

	ms, errs := e.app.Marketplaces(ctx)
	if len(ms) != 1 || ms[0].Where != home || len(errs) != 1 || !strings.Contains(errs[0].Error(), "left out") {
		t.Errorf("marketplaces %+v, %v", ms, errs)
	}
	if _, _, err := e.app.AddMarketplace(ctx, other); err == nil || !strings.Contains(err.Error(), "named acme") {
		t.Errorf("a marketplace of a name in use was added: %v", err)
	}
}
