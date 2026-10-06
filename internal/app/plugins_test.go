package app

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"testing"

	"github.com/voocel/codebot/internal/agent/subagent"
	"github.com/voocel/codebot/internal/extension"
	"github.com/voocel/codebot/internal/infra/config"
)

const kitManifest = `{"$schema": "https://agent-plugins.org/schemas/1.0.0/plugin.schema.json", "name": "kit"}`

// writeKit writes the plugin kit into dir: a skill, and an MCP server
// when db.
func writeKit(t *testing.T, dir string, db bool) {
	t.Helper()
	writeSkill(t, filepath.Join(dir, "skills"), "release", "---\ndescription: releases\n---\nRelease.\n")
	if err := os.WriteFile(filepath.Join(dir, "plugin.json"), []byte(kitManifest), 0o644); err != nil {
		t.Fatal(err)
	}
	if db {
		mcp := `{"$schema": "https://agent-plugins.org/schemas/1.0.0/mcp.schema.json", "mcpServers": {"db": {"type": "stdio", "command": "db-mcp"}}}`
		if err := os.WriteFile(filepath.Join(dir, "mcp.json"), []byte(mcp), 0o644); err != nil {
			t.Fatal(err)
		}
	}
}

func skillNames(e *env) []string {
	var names []string
	for _, s := range e.app.Current().Skills() {
		names = append(names, s.Name)
	}
	return names
}

// The user adds a plugin of theirs, turns it off here and removes it.
func TestPluginLifecycle(t *testing.T) {
	e := boot(t, setup{}, map[string]*fakeModel{"claude-sonnet-4-5": script()})
	dir := filepath.Join(os.Getenv("HOME"), "kit")
	writeKit(t, dir, true)
	ctx := context.Background()

	o, err := e.app.OfferPlugin(ctx, "~/kit", false)
	if err != nil {
		t.Fatal(err)
	}
	if o.Name != "kit" || !slices.Equal(o.Surface, Surface{{Kind: "mcp", Detail: "kit_db: db-mcp"}}) {
		t.Fatalf("offer %+v", o)
	}
	if _, err := e.app.AddPlugin(ctx, o); err != nil {
		t.Fatal(err)
	}
	if layers, _ := config.Load(e.cwd); !slices.Equal(layers.User.Plugins, []string{"~/kit"}) {
		t.Errorf("the user's plugins %q", layers.User.Plugins)
	}
	if !slices.Contains(skillNames(e), "kit:release") {
		t.Errorf("skills %q", skillNames(e))
	}
	if _, ok := e.app.Extensions().MCPConfig()["kit_db"]; !ok {
		t.Error("the plugin's MCP server is not configured")
	}
	if _, err := e.app.OfferPlugin(ctx, "~/kit", false); err == nil {
		t.Error("the plugin was offered twice")
	}

	if _, err := e.app.SetPluginEnabled(ctx, "kit", false); err != nil {
		t.Fatal(err)
	}
	if slices.Contains(skillNames(e), "kit:release") || e.app.Plugins()[0].State != PluginOff {
		t.Errorf("turned off, skills %q, plugin %+v", skillNames(e), e.app.Plugins()[0])
	}
	if _, err := e.app.RemovePlugin(ctx, "kit"); err != nil {
		t.Fatal(err)
	}
	if len(e.app.Plugins()) > 0 || slices.Contains(skillNames(e), "kit:release") {
		t.Errorf("removed, plugins %+v", e.app.Plugins())
	}
	if o, err = e.app.OfferPlugin(ctx, "~/kit", false); err != nil {
		t.Fatal(err)
	}
	if _, err := e.app.AddPlugin(ctx, o); err != nil {
		t.Fatal(err)
	}
	if pl := e.app.Plugins()[0]; pl.State != PluginOn {
		t.Errorf("added again after it was turned off, it is %s", pl.State)
	}
}

// remote serves dir as the git repository https://example.test/acme/kit,
// whose history the returned function adds to.
func remote(t *testing.T) (repo string, commit func(msg string)) {
	repo = t.TempDir()
	git := func(args ...string) {
		cmd := exec.Command("git", append([]string{"-c", "user.name=t", "-c", "user.email=t@t"}, args...)...)
		cmd.Dir = repo
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, out)
		}
	}
	git("init", "-q", "-b", "main")
	t.Setenv("GIT_CONFIG_COUNT", "2")
	t.Setenv("GIT_CONFIG_KEY_0", "url.file://"+repo+".insteadOf")
	t.Setenv("GIT_CONFIG_VALUE_0", "https://example.test/acme/kit")
	t.Setenv("GIT_CONFIG_KEY_1", "protocol.file.allow")
	t.Setenv("GIT_CONFIG_VALUE_1", "always")
	return repo, func(msg string) {
		git("add", "-A")
		git("commit", "-qm", msg)
	}
}

// A git plugin is locked at the commit the user agreed to; an update that
// runs something new waits for them to agree again.
func TestGitPluginUpdates(t *testing.T) {
	repo, commit := remote(t)
	writeKit(t, repo, false)
	commit("v1")
	e := boot(t, setup{}, map[string]*fakeModel{"claude-sonnet-4-5": script()})
	ctx := context.Background()

	o, err := e.app.OfferPlugin(ctx, "example.test/acme/kit#main", false)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := e.app.AddPlugin(ctx, o); err != nil {
		t.Fatal(err)
	}
	if pl := e.app.Plugins()[0]; pl.State != PluginOn || pl.Commit != o.Commit {
		t.Fatalf("plugin %+v", pl)
	}

	if err := os.WriteFile(filepath.Join(repo, "README.md"), []byte("docs"), 0o644); err != nil {
		t.Fatal(err)
	}
	commit("docs")
	updates, err := e.app.UpdatePlugins(ctx, "")
	if err != nil || len(updates) != 1 || !updates[0].Applied {
		t.Fatalf("updates %+v, %v", updates, err)
	}
	quiet := updates[0].Commit

	writeKit(t, repo, true)
	commit("db")
	updates, err = e.app.UpdatePlugins(ctx, "kit")
	if err != nil || len(updates) != 1 || updates[0].Applied || !slices.Equal(updates[0].Added, Surface{{Kind: "mcp", Detail: "kit_db: db-mcp"}}) {
		t.Fatalf("updates %+v, %v", updates, err)
	}
	if pl := e.app.Plugins()[0]; pl.Commit != quiet || len(pl.MCP) > 0 {
		t.Fatalf("the update ran something new unasked: %+v", pl)
	}
	if _, err := e.app.ApplyUpdate(ctx, updates[0]); err != nil {
		t.Fatal(err)
	}
	if pl := e.app.Plugins()[0]; pl.Commit != updates[0].Commit || len(pl.MCP) != 1 {
		t.Fatalf("plugin %+v", pl)
	}
	if updates, _ := e.app.UpdatePlugins(ctx, ""); updates[0].Commit != e.app.Plugins()[0].Commit {
		t.Errorf("an update of nothing new moved the plugin: %+v", updates[0])
	}
}

// A project's git plugins are fetched once the user trusts it; what they
// run is asked about before it does.
func TestProjectPluginsFetchOnceTrusted(t *testing.T) {
	repo, commit := remote(t)
	writeKit(t, repo, true)
	commit("v1")
	e := boot(t, setup{project: map[string]any{"plugins": []string{"example.test/acme/kit"}}}, map[string]*fakeModel{"claude-sonnet-4-5": script()})
	ctx := context.Background()
	if pl := e.app.Plugins()[0]; pl.State != PluginHeld {
		t.Fatalf("the untrusted project's plugin is %s", pl.State)
	}
	if r := e.app.Connect(ctx); len(r.Fetched) > 0 {
		t.Fatalf("the untrusted project's plugin was fetched: %+v", r)
	}
	r, err := e.app.SetTrust(ctx, e.app.Trust().Surface, true, false)
	if err != nil || !slices.Equal(r.Fetched, []string{"example.test/acme/kit"}) {
		t.Fatalf("trusted, %+v, %v", r, err)
	}
	trust := e.app.Trust()
	if trust.Trusted || !slices.Equal(trust.Ask, Surface{{Kind: "mcp", Detail: "kit_db: db-mcp"}}) {
		t.Fatalf("what the plugin runs went unasked: %+v", trust)
	}
	if _, err := e.app.SetTrust(ctx, trust.Surface, true, false); err != nil {
		t.Fatal(err)
	}
	if pl := e.app.Plugins()[0]; pl.State != PluginOn || !slices.Contains(skillNames(e), "kit:release") {
		t.Errorf("plugin %+v, skills %q", pl, skillNames(e))
	}

	if _, err := e.app.SetTrust(ctx, e.app.Trust().Surface, false, false); err != nil {
		t.Fatal(err)
	}
	if updates, err := e.app.UpdatePlugins(ctx, ""); err == nil {
		t.Errorf("the untrusted project's plugin was fetched anew: %+v", updates)
	}
}

// A git plugin no longer cached comes back at the commit it is locked at,
// not at its ref's.
func TestLockedPluginsComeBackAtTheirCommit(t *testing.T) {
	repo, commit := remote(t)
	writeKit(t, repo, false)
	commit("v1")
	e := boot(t, setup{}, map[string]*fakeModel{"claude-sonnet-4-5": script()})
	ctx := context.Background()
	o, err := e.app.OfferPlugin(ctx, "example.test/acme/kit", false)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := e.app.AddPlugin(ctx, o); err != nil {
		t.Fatal(err)
	}
	writeKit(t, repo, true)
	commit("db")
	if err := os.RemoveAll(extension.CacheDir()); err != nil {
		t.Fatal(err)
	}
	r, err := e.app.Reload(ctx)
	if err != nil || len(r.Fetched) != 1 {
		t.Fatalf("reloaded, %+v, %v", r, err)
	}
	if pl := e.app.Plugins()[0]; pl.State != PluginOn || pl.Commit != o.Commit || len(pl.MCP) > 0 {
		t.Errorf("plugin %+v", pl)
	}
}

// A plugin the user's settings declare but never fetched is added as any
// other, declared once.
func TestDeclaredPluginsAreAdded(t *testing.T) {
	repo, commit := remote(t)
	writeKit(t, repo, false)
	commit("v1")
	e := boot(t, setup{settings: map[string]any{"plugins": []string{"example.test/acme/kit"}}}, map[string]*fakeModel{"claude-sonnet-4-5": script()})
	ctx := context.Background()
	if r := e.app.Connect(ctx); len(r.Fetched) > 0 || e.app.Plugins()[0].State != PluginMissing {
		t.Fatalf("a plugin the user never agreed to was fetched: %+v", r)
	}
	o, err := e.app.OfferPlugin(ctx, "example.test/acme/kit", false)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := e.app.AddPlugin(ctx, o); err != nil {
		t.Fatal(err)
	}
	if layers, _ := config.Load(e.cwd); len(layers.User.Plugins) != 1 || e.app.Plugins()[0].State != PluginOn {
		t.Errorf("plugins %q, %+v", layers.User.Plugins, e.app.Plugins())
	}
}

// A plugin added to a project the user trusts for good is trusted for
// good: the trust kept takes what it runs.
func TestRememberedTrustTakesAddedPlugins(t *testing.T) {
	e := boot(t, setup{git: true, project: map[string]any{}}, map[string]*fakeModel{"claude-sonnet-4-5": script()})
	writeKit(t, filepath.Join(e.cwd, "tools", "kit"), true)
	ctx := context.Background()
	if _, err := e.app.SetTrust(ctx, e.app.Trust().Surface, true, true); err != nil {
		t.Fatal(err)
	}
	o, err := e.app.OfferPlugin(ctx, "tools/kit", true)
	if err == nil {
		t.Fatal("a bare path was taken for a directory")
	}
	if o, err = e.app.OfferPlugin(ctx, "./tools/kit", true); err != nil {
		t.Fatal(err)
	}
	if o.Source != "../tools/kit" {
		t.Errorf("declared as %q, not from the settings' directory", o.Source)
	}
	if _, err := e.app.AddPlugin(ctx, o); err != nil {
		t.Fatal(err)
	}
	ws, err := extension.ReadWorkspace(e.cwd)
	if err != nil {
		t.Fatal(err)
	}
	if want := (Surface{{Kind: "mcp", Detail: "kit_db: db-mcp"}, {Kind: "plugin", Detail: "../tools/kit"}}); !slices.Equal(ws.Trust.Surface, want) {
		t.Errorf("trust kept %q", ws.Trust.Surface)
	}
	if !e.app.Trust().Trusted || e.app.Plugins()[0].State != PluginOn {
		t.Errorf("trust %+v, plugin %+v", e.app.Trust(), e.app.Plugins()[0])
	}
}

// Of two plugins of one name, both are listed, and a name for both names
// neither.
func TestPluginsOfOneName(t *testing.T) {
	e := boot(t, setup{git: true, settings: map[string]any{"plugins": []string{"~/kit"}}, project: map[string]any{"plugins": []string{"../tools/kit"}}}, map[string]*fakeModel{"claude-sonnet-4-5": script()})
	writeKit(t, filepath.Join(os.Getenv("HOME"), "kit"), false)
	writeKit(t, filepath.Join(e.cwd, "tools", "kit"), false)
	ctx := context.Background()
	if _, err := e.app.SetTrust(ctx, e.app.Trust().Surface, true, false); err != nil {
		t.Fatal(err)
	}
	var states []string
	for _, pl := range e.app.Plugins() {
		states = append(states, pl.Source+" "+string(pl.State))
	}
	if want := []string{"../tools/kit on", "~/kit shadowed"}; !slices.Equal(states, want) {
		t.Fatalf("plugins %q", states)
	}
	if _, err := e.app.RemovePlugin(ctx, "kit"); err == nil {
		t.Fatal("removed one of two plugins named kit")
	}
	if _, err := e.app.RemovePlugin(ctx, "~/kit"); err != nil {
		t.Fatal(err)
	}
	if layers, _ := config.Load(e.cwd); len(layers.User.Plugins) > 0 || len(layers.Project.Plugins) != 1 {
		t.Errorf("user's %q, project's %q", layers.User.Plugins, layers.Project.Plugins)
	}
}

// A plugin's hooks run in the conversation, PLUGIN_DATA theirs; its agents
// join the others.
func TestPluginHooksRun(t *testing.T) {
	e := boot(t, setup{}, map[string]*fakeModel{"claude-sonnet-4-5": script(text("ok"))})
	dir := filepath.Join(os.Getenv("HOME"), "kit")
	writeKit(t, dir, false)
	manifest := `{"$schema": "https://agent-plugins.org/schemas/1.0.0/plugin.schema.json", "name": "kit",
		"extensions": {"io.github.voocel.codebot": {
			"hooks": {"UserPromptSubmit": [{"type": "command", "command": "touch \"$PLUGIN_DATA/ran\""}]},
			"agents": "./agents"
		}}}`
	if err := os.WriteFile(filepath.Join(dir, "plugin.json"), []byte(manifest), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(dir, "agents"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "agents", "reviewer.md"), []byte("---\ndescription: Reviews\n---\nReview.\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	o, err := e.app.OfferPlugin(ctx, "~/kit", false)
	if err != nil {
		t.Fatal(err)
	}
	if !slices.Contains(o.Surface, Surface{{Kind: "hook", Detail: `kit: UserPromptSubmit: touch "$PLUGIN_DATA/ran"`}}[0]) {
		t.Errorf("the offer hides the hook: %q", o.Surface)
	}
	if _, err := e.app.AddPlugin(ctx, o); err != nil {
		t.Fatal(err)
	}
	if !slices.ContainsFunc(e.app.Extensions().Agents, func(d subagent.AgentDefinition) bool { return d.Name == "kit:reviewer" }) {
		t.Errorf("agents %+v", e.app.Extensions().Agents)
	}
	e.submit("go")
	if _, err := os.Stat(filepath.Join(extension.DataDir(), "kit", "ran")); err != nil {
		t.Errorf("the plugin's hook did not run with its data: %v", err)
	}
}
