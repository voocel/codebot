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

// writeKit writes a plugin with one skill and the named MCP servers.
func writeKit(t *testing.T, dir string, servers ...string) {
	t.Helper()
	writeSkill(t, filepath.Join(dir, "skills"), "release", "---\ndescription: releases\n---\nRelease.\n")
	if err := os.WriteFile(filepath.Join(dir, "plugin.json"), []byte(kitManifest), 0o644); err != nil {
		t.Fatal(err)
	}
	if len(servers) == 0 {
		return
	}
	mcp := `{"$schema": "https://agent-plugins.org/schemas/1.0.0/mcp.schema.json", "mcpServers": {`
	for i, name := range servers {
		if i > 0 {
			mcp += ", "
		}
		mcp += `"` + name + `": {"type": "stdio", "command": "` + name + `-mcp"}`
	}
	if err := os.WriteFile(filepath.Join(dir, "mcp.json"), []byte(mcp+"}}"), 0o644); err != nil {
		t.Fatal(err)
	}
}

func mcpDetail(name string) string { return "kit_" + name + ": " + name + "-mcp" }

func details(s Surface) []string {
	var out []string
	for _, it := range s {
		out = append(out, it.Detail)
	}
	return out
}

func skillNames(e *env) []string {
	var names []string
	for _, s := range e.app.Current().Skills() {
		names = append(names, s.Name)
	}
	return names
}

func servers(e *env) []string {
	var out []string
	for _, srv := range e.app.Extensions().MCP {
		out = append(out, srv.Name)
	}
	return out
}

// Removing a plugin keeps its consent, since another project may still
// declare it.
func TestPluginLifecycle(t *testing.T) {
	e := boot(t, setup{}, map[string]*fakeModel{"claude-sonnet-4-5": script()})
	writeKit(t, filepath.Join(os.Getenv("HOME"), "kit"), "db")
	ctx := context.Background()

	o, err := e.app.OfferPlugin(ctx, "~/kit", false)
	if err != nil {
		t.Fatal(err)
	}
	if o.Name != "kit" || !slices.Equal(details(o.Surface), []string{mcpDetail("db")}) || !slices.Equal(o.New, o.Surface) {
		t.Fatalf("offer %+v", o)
	}
	if _, err := e.app.AcceptPlugin(ctx, o, o.New); err != nil {
		t.Fatal(err)
	}
	if layers, _ := config.Load(e.cwd); !slices.Equal(layers.User.Plugins, []string{"~/kit"}) {
		t.Errorf("the user's plugins %q", layers.User.Plugins)
	}
	if !slices.Contains(skillNames(e), "kit:release") || !slices.Equal(servers(e), []string{"kit_db"}) {
		t.Errorf("skills %q, servers %q", skillNames(e), servers(e))
	}
	if _, err := e.app.OfferPlugin(ctx, "~/kit", false); err == nil {
		t.Error("the plugin was offered twice")
	}

	if _, err := e.app.RemovePlugin(ctx, "kit", false); err != nil {
		t.Fatal(err)
	}
	if len(e.app.Plugins()) > 0 || slices.Contains(skillNames(e), "kit:release") {
		t.Errorf("removed, plugins %+v", e.app.Plugins())
	}
	if c, _ := extension.ReadConsents(); len(c.Plugins) != 1 {
		t.Errorf("what the user agreed to of the plugin removed is forgotten: %+v", c.Plugins)
	}
}

// A local plugin is reread on reload. New items wait for consent while the
// agreed ones keep running.
func TestLocalPluginsWaitForWhatTheyAdd(t *testing.T) {
	e := boot(t, setup{}, map[string]*fakeModel{"claude-sonnet-4-5": script()})
	dir := filepath.Join(os.Getenv("HOME"), "kit")
	writeKit(t, dir, "db")
	ctx := context.Background()
	o, err := e.app.OfferPlugin(ctx, dir, false)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := e.app.AcceptPlugin(ctx, o, o.New); err != nil {
		t.Fatal(err)
	}

	writeKit(t, dir, "db", "shell")
	if _, err := e.app.Reload(ctx); err != nil {
		t.Fatal(err)
	}
	if pl := e.app.Plugins()[0]; pl.State != PluginOn || !slices.Equal(details(pl.Held()), []string{mcpDetail("shell")}) {
		t.Fatalf("plugin %+v", pl)
	}
	if !slices.Equal(servers(e), []string{"kit_db"}) {
		t.Fatalf("servers %q", servers(e))
	}
	offers, fetched, errs := e.app.InstallPlugins(ctx)
	if len(offers) != 1 || len(fetched) > 0 || len(errs) > 0 || !slices.Equal(details(offers[0].New), []string{mcpDetail("shell")}) {
		t.Fatalf("install: %+v, %q, %v", offers, fetched, errs)
	}
	if _, err := e.app.AcceptPlugin(ctx, offers[0], offers[0].New); err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(servers(e), []string{"kit_db", "kit_shell"}) || len(e.app.Plugins()[0].Held()) > 0 {
		t.Errorf("servers %q, plugin %+v", servers(e), e.app.Plugins()[0])
	}
	if layers, _ := config.Load(e.cwd); len(layers.User.Plugins) != 1 {
		t.Errorf("declared again: %q", layers.User.Plugins)
	}
}

// remote serves a git repository at https://example.test/acme/kit; commit
// adds to its history.
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

// A git plugin stays at its agreed commit. A moved ref is reported as an
// update without fetching, and an update that runs something new waits for
// consent.
func TestGitPluginUpdates(t *testing.T) {
	repo, commit := remote(t)
	writeKit(t, repo)
	commit("v1")
	e := boot(t, setup{}, map[string]*fakeModel{"claude-sonnet-4-5": script()})
	ctx := context.Background()

	o, err := e.app.OfferPlugin(ctx, "example.test/acme/kit#main", false)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := e.app.AcceptPlugin(ctx, o, o.New); err != nil {
		t.Fatal(err)
	}
	if pl := e.app.Plugins()[0]; pl.State != PluginOn || pl.Commit != o.Commit {
		t.Fatalf("plugin %+v", pl)
	}
	if names := e.app.PluginUpdates(ctx); len(names) > 0 {
		t.Fatalf("up to date, updates for %q", names)
	}

	if err := os.WriteFile(filepath.Join(repo, "README.md"), []byte("docs"), 0o644); err != nil {
		t.Fatal(err)
	}
	commit("docs")
	if names := e.app.PluginUpdates(ctx); !slices.Equal(names, []string{"kit"}) {
		t.Fatalf("updates for %q", names)
	}
	if pl := e.app.Plugins()[0]; pl.Commit != o.Commit {
		t.Fatalf("checking for updates moved the plugin to %s", pl.Commit)
	}
	updates, err := e.app.UpdatePlugins(ctx, "")
	if err != nil || len(updates) != 1 || updates[0].Offer != nil || updates[0].Commit == o.Commit || e.app.Plugins()[0].Commit != updates[0].Commit {
		t.Fatalf("an update of nothing new did not apply: %+v, %v", updates, err)
	}
	quiet := updates[0].Commit

	writeKit(t, repo, "db")
	commit("db")
	updates, err = e.app.UpdatePlugins(ctx, "kit")
	if err != nil || len(updates) != 1 || updates[0].Offer == nil || !slices.Equal(details(updates[0].Offer.New), []string{mcpDetail("db")}) {
		t.Fatalf("updates %+v, %v", updates, err)
	}
	if pl := e.app.Plugins()[0]; pl.Commit != quiet || len(servers(e)) > 0 {
		t.Fatalf("the update ran something new unasked: %+v", pl)
	}
	if _, err := e.app.AcceptPlugin(ctx, updates[0].Offer, updates[0].Offer.New); err != nil {
		t.Fatal(err)
	}
	if pl := e.app.Plugins()[0]; pl.Commit != updates[0].Commit || !slices.Equal(servers(e), []string{"kit_db"}) {
		t.Fatalf("plugin %+v", pl)
	}
	if updates, _ := e.app.UpdatePlugins(ctx, ""); updates[0].Commit != e.app.Plugins()[0].Commit || updates[0].Offer != nil {
		t.Errorf("up to date, %+v", updates[0])
	}
}

// A project's plugins need project trust and are fetched only on install.
func TestProjectPlugins(t *testing.T) {
	repo, commit := remote(t)
	writeKit(t, repo, "db")
	commit("v1")
	e := boot(t, setup{project: map[string]any{"plugins": []string{"example.test/acme/kit"}}}, map[string]*fakeModel{"claude-sonnet-4-5": script()})
	ctx := context.Background()
	if pl := e.app.Plugins()[0]; pl.State != PluginUntrusted {
		t.Fatalf("the untrusted project's plugin is %s", pl.State)
	}
	if _, _, errs := e.app.InstallPlugins(ctx); len(errs) > 0 || e.app.Plugins()[0].State != PluginUntrusted {
		t.Fatalf("installed the untrusted project's plugin: %v", errs)
	}
	if _, err := e.app.SetTrust(ctx, e.app.Trust().Surface, e.app.Trust().Surface); err != nil {
		t.Fatal(err)
	}
	if pl := e.app.Plugins()[0]; pl.State != PluginNotInstalled || len(e.app.Trust().Ask()) > 0 {
		t.Fatalf("trusted, plugin %+v, trust %+v", pl, e.app.Trust())
	}
	offers, _, errs := e.app.InstallPlugins(ctx)
	if len(offers) != 1 || len(errs) > 0 || !slices.Equal(details(offers[0].New), []string{mcpDetail("db")}) {
		t.Fatalf("install: %+v, %v", offers, errs)
	}
	if pl := e.app.Plugins()[0]; pl.State != PluginNotInstalled {
		t.Fatalf("offered, the plugin is %s", pl.State)
	}
	if _, err := e.app.AcceptPlugin(ctx, offers[0], offers[0].New); err != nil {
		t.Fatal(err)
	}
	if pl := e.app.Plugins()[0]; pl.State != PluginOn || !slices.Equal(servers(e), []string{"kit_db"}) {
		t.Errorf("plugin %+v, servers %q", pl, servers(e))
	}

	if _, err := e.app.DenyTrust(ctx); err != nil {
		t.Fatal(err)
	}
	if pl := e.app.Plugins()[0]; pl.State != PluginUntrusted || len(servers(e)) > 0 {
		t.Errorf("distrusted, plugin %+v, servers %q", pl, servers(e))
	}
}

// A git plugin missing from the cache is refetched at its agreed commit, not
// its ref, without asking.
func TestAgreedPluginsComeBackAtTheirCommit(t *testing.T) {
	repo, commit := remote(t)
	writeKit(t, repo)
	commit("v1")
	e := boot(t, setup{}, map[string]*fakeModel{"claude-sonnet-4-5": script()})
	ctx := context.Background()
	o, err := e.app.OfferPlugin(ctx, "example.test/acme/kit", false)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := e.app.AcceptPlugin(ctx, o, o.New); err != nil {
		t.Fatal(err)
	}
	writeKit(t, repo, "db")
	commit("db")
	if err := os.RemoveAll(filepath.Join(config.UserConfigDir(), "plugins", "cache")); err != nil {
		t.Fatal(err)
	}
	if _, err := e.app.Reload(ctx); err != nil {
		t.Fatal(err)
	}
	if pl := e.app.Plugins()[0]; pl.State != PluginNotCached || pl.Commit != o.Commit {
		t.Fatalf("plugin %+v", pl)
	}
	offers, fetched, errs := e.app.InstallPlugins(ctx)
	if len(offers) > 0 || !slices.Equal(fetched, []string{"example.test/acme/kit"}) || len(errs) > 0 {
		t.Fatalf("install: %+v, %q, %v", offers, fetched, errs)
	}
	if pl := e.app.Plugins()[0]; pl.State != PluginOn || pl.Commit != o.Commit || len(servers(e)) > 0 {
		t.Errorf("plugin %+v", pl)
	}
}

// A declared plugin the user never agreed to is not fetched until installed.
func TestDeclaredPluginsWaitToBeInstalled(t *testing.T) {
	repo, commit := remote(t)
	writeKit(t, repo)
	commit("v1")
	e := boot(t, setup{settings: map[string]any{"plugins": []string{"example.test/acme/kit"}}}, map[string]*fakeModel{"claude-sonnet-4-5": script()})
	ctx := context.Background()
	e.app.Connect(ctx)
	if pl := e.app.Plugins()[0]; pl.State != PluginNotInstalled || pl.Commit != "" {
		t.Fatalf("plugin %+v", pl)
	}
	if _, err := e.app.OfferPlugin(ctx, "example.test/acme/kit", false); err == nil {
		t.Error("a plugin declared already was offered to add")
	}
	offers, _, errs := e.app.InstallPlugins(ctx)
	if len(offers) != 1 || len(errs) > 0 {
		t.Fatalf("install: %+v, %v", offers, errs)
	}
	if _, err := e.app.AcceptPlugin(ctx, offers[0], offers[0].New); err != nil {
		t.Fatal(err)
	}
	if layers, _ := config.Load(e.cwd); len(layers.User.Plugins) != 1 || e.app.Plugins()[0].State != PluginOn {
		t.Errorf("plugins %q, %+v", layers.User.Plugins, e.app.Plugins())
	}
}

// A plugin the user adds to a trusted project counts as trusted, since they
// wrote it there.
func TestPluginsAddedToTheProject(t *testing.T) {
	e := boot(t, setup{git: true, project: map[string]any{"permissions": map[string]any{"allow": []string{"Bash(make *)"}}}}, map[string]*fakeModel{"claude-sonnet-4-5": script()})
	writeKit(t, filepath.Join(e.cwd, "tools", "kit"), "db")
	ctx := context.Background()
	if _, err := e.app.SetTrust(ctx, e.app.Trust().Surface, e.app.Trust().Surface); err != nil {
		t.Fatal(err)
	}
	if _, err := e.app.OfferPlugin(ctx, "tools/kit", true); err == nil {
		t.Fatal("a bare path was taken for a directory")
	}
	o, err := e.app.OfferPlugin(ctx, "./tools/kit", true)
	if err != nil {
		t.Fatal(err)
	}
	if o.Source != "../tools/kit" {
		t.Errorf("declared as %q, not from the settings' directory", o.Source)
	}
	if _, err := e.app.AcceptPlugin(ctx, o, o.New); err != nil {
		t.Fatal(err)
	}
	c, err := extension.ReadConsents()
	if err != nil {
		t.Fatal(err)
	}
	kept := c.Projects[e.cwd].Surface
	if want := (Surface{extension.NewItem("allow", "Bash(make *)"), extension.NewItem("plugin", "../tools/kit")}); len(kept) != len(want) || !kept.HasAll(want) {
		t.Errorf("trust kept %q", kept)
	}
	if len(e.app.Trust().Ask()) > 0 || e.app.Plugins()[0].State != PluginOn || !slices.Equal(servers(e), []string{"kit_db"}) {
		t.Errorf("trust %+v, plugin %+v", e.app.Trust(), e.app.Plugins()[0])
	}
}

// Two plugins with one name are both listed, with the project's on. Each is
// removed from its own settings.
func TestPluginsOfOneName(t *testing.T) {
	e := boot(t, setup{git: true, settings: map[string]any{"plugins": []string{"~/kit"}}, project: map[string]any{"plugins": []string{"../tools/kit"}}}, map[string]*fakeModel{"claude-sonnet-4-5": script()})
	writeKit(t, filepath.Join(os.Getenv("HOME"), "kit"))
	writeKit(t, filepath.Join(e.cwd, "tools", "kit"))
	ctx := context.Background()
	if _, err := e.app.SetTrust(ctx, e.app.Trust().Surface, e.app.Trust().Surface); err != nil {
		t.Fatal(err)
	}
	var states []string
	for _, pl := range e.app.Plugins() {
		states = append(states, pl.Source+" "+string(pl.State))
	}
	if want := []string{"../tools/kit on", "~/kit shadowed"}; !slices.Equal(states, want) {
		t.Fatalf("plugins %q", states)
	}
	if _, err := e.app.RemovePlugin(ctx, "kit", false); err != nil {
		t.Fatal(err)
	}
	if layers, _ := config.Load(e.cwd); len(layers.User.Plugins) > 0 || len(layers.Project.Plugins) != 1 {
		t.Errorf("user's %q, project's %q", layers.User.Plugins, layers.Project.Plugins)
	}
}

// A command-line plugin runs everything for the session, is reread on
// reload, and leaves no consent behind.
func TestSessionPlugins(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "kit")
	writeKit(t, dir, "db")
	e := boot(t, setup{plugins: []string{dir}}, map[string]*fakeModel{"claude-sonnet-4-5": script()})
	if pl := e.app.Plugins()[0]; pl.State != PluginOn || pl.Scope != extension.Session || !slices.Equal(servers(e), []string{"kit_db"}) {
		t.Fatalf("plugin %+v, servers %q", pl, servers(e))
	}
	writeKit(t, dir, "db", "shell")
	if _, err := e.app.Reload(context.Background()); err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(servers(e), []string{"kit_db", "kit_shell"}) {
		t.Errorf("servers %q", servers(e))
	}
	if c, _ := extension.ReadConsents(); c.Plugins != nil {
		t.Errorf("kept %+v", c)
	}
}

// A plugin's hooks run with their own PLUGIN_DATA, and its agents join the
// other agents.
func TestPluginHooksRun(t *testing.T) {
	e := boot(t, setup{}, map[string]*fakeModel{"claude-sonnet-4-5": script(text("ok"))})
	dir := filepath.Join(os.Getenv("HOME"), "kit")
	writeKit(t, dir)
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
	if !slices.Contains(details(o.Surface), `kit: UserPromptSubmit: touch "$PLUGIN_DATA/ran"`) {
		t.Errorf("the offer hides the hook: %q", o.Surface)
	}
	if _, err := e.app.AcceptPlugin(ctx, o, o.New); err != nil {
		t.Fatal(err)
	}
	if !slices.ContainsFunc(e.app.Extensions().Agents, func(d subagent.AgentDefinition) bool { return d.Name == "kit:reviewer" }) {
		t.Errorf("agents %+v", e.app.Extensions().Agents)
	}
	e.submit("go")
	if _, err := os.Stat(filepath.Join(e.app.Plugins()[0].Data, "ran")); err != nil {
		t.Errorf("the plugin's hook did not run with its data: %v", err)
	}
}
