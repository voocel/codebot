package plugin

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/voocel/codebot/internal/infra/config"
)

func write(t *testing.T, path, text string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(text), 0o755); err != nil {
		t.Fatal(err)
	}
}

const manifest = `{"$schema": "https://agent-plugins.org/schemas/1.0.0/plugin.schema.json", "name": "acme-tools", "version": "1.2.0"`

// plugin writes a plugin of the given files into a new directory.
func plugin(t *testing.T, files map[string]string) string {
	dir := t.TempDir()
	for name, text := range files {
		write(t, filepath.Join(dir, name), text)
	}
	return dir
}

func TestReadsAPlugin(t *testing.T) {
	dir := plugin(t, map[string]string{
		"plugin.json":             manifest + `, "description": "Tools", "author": {"name": "Acme"}, "extensions": {"com.example": {"x": 1}}}`,
		"skills/release/SKILL.md": "---\ndescription: releases\n---\nRun !`make release`\n",
		"skills/empty/README.md":  "not a skill",
		"skills/deep/x/SKILL.md":  "---\ndescription: too deep\n---\n",
		"skills/broken/SKILL.md":  "---\nname: Not A Name!\n---\n",
		"bin/db":                  "#!/bin/sh\n",
		"mcp.json": `{"$schema": "https://agent-plugins.org/schemas/1.0.0/mcp.schema.json", "mcpServers": {
			"db": {"type": "stdio", "command": "./bin/db", "args": ["--data", "${PLUGIN_DATA}/db", "${HOME}"], "env": {"CONFIG": "${PLUGIN_ROOT}/c.json"}},
			"api": {"type": "streamable-http", "url": "https://api.example/mcp", "headers": {"X-Tenant": "${TENANT}"}},
			"old": {"type": "sse", "url": "https://old.example/sse"},
			"npx": {"type": "stdio", "command": "npx", "cwd": "${PLUGIN_DATA}"}
		}}`,
	})
	p, problems, err := Read(dir, "/data/acme-tools")
	if err != nil {
		t.Fatal(err)
	}
	root, _ := filepath.EvalSymlinks(dir)
	if p.Name != "acme-tools" || p.Version != "1.2.0" || p.Description != "Tools" || p.Root != root || p.Data != "/data/acme-tools" {
		t.Errorf("plugin %+v", p)
	}
	if len(p.Skills) != 1 || p.Skills[0].Name != "release" || len(p.Skills[0].Privileges()) != 1 {
		t.Errorf("skills %+v", p.Skills)
	}
	want := map[string]config.MCPServer{
		"db": {Type: "stdio", Command: filepath.Join(root, "bin", "db"), Cwd: root,
			Args: []string{"--data", "/data/acme-tools/db", "${HOME}"},
			Env:  map[string]string{"CONFIG": root + "/c.json", "PLUGIN_ROOT": root, "PLUGIN_DATA": "/data/acme-tools"}},
		"api": {Type: "http", URL: "https://api.example/mcp", Headers: map[string]string{"X-Tenant": "${TENANT}"}},
		"npx": {Type: "stdio", Command: "npx", Cwd: "/data/acme-tools", Env: map[string]string{"PLUGIN_ROOT": root, "PLUGIN_DATA": "/data/acme-tools"}},
	}
	if got, want := fmt.Sprint(p.MCP), fmt.Sprint(want); got != want {
		t.Errorf("MCP\n got %s\nwant %s", got, want)
	}
	if got := fmt.Sprint(problems); !strings.Contains(got, "broken") || !strings.Contains(got, "sse") || len(problems) != 2 {
		t.Errorf("problems %v", problems)
	}
}

func TestManifestViolations(t *testing.T) {
	for _, c := range []struct {
		manifest string
		fatal    bool
	}{
		{`{"name": "acme"}`, true},
		{`{"$schema": "https://agent-plugins.org/schemas/2.0.0/plugin.schema.json", "name": "acme"}`, true},
		{manifest[:strings.Index(manifest, `"name"`)] + `"name": "Acme"}`, true},
		{manifest[:strings.Index(manifest, `"name"`)] + `"name": "a--b"}`, true},
		{manifest[:strings.Index(manifest, `"name"`)] + `"name": "a_b"}`, true},
		{manifest + `, "version": 2}`, true},
		{manifest + `, "author": {"name": "a", "phone": "1"}}`, true},
		{manifest + `, "extensions": {"com.example": 1}}`, true},
		{manifest + `, "hooks": {}}`, false},
		{manifest + `, "extensions": []}`, false},
	} {
		_, problems, err := Read(plugin(t, map[string]string{"plugin.json": c.manifest}), "/data")
		if (err != nil) != c.fatal || !c.fatal && len(problems) != 1 {
			t.Errorf("%s: err %v, problems %v", c.manifest, err, problems)
		}
	}
}

func TestMCPViolations(t *testing.T) {
	for _, server := range []string{
		`{"type": "stdio", "command": "npx -y x"}`,
		`{"type": "stdio", "command": "/bin/sh"}`,
		`{"type": "stdio", "command": "${PLUGIN_ROOT}/bin/db"}`,
		`{"type": "stdio", "command": "./../escape"}`,
		`{"type": "stdio", "command": "npx", "env": {"PLUGIN_ROOT": "/x"}}`,
		`{"type": "stdio", "command": "npx", "cwd": "/tmp"}`,
		`{"type": "stdio", "command": "npx", "cwd": "${PLUGIN_DATA}/../x"}`,
		`{"type": "stdio", "command": "npx", "url": "https://x"}`,
		`{"type": "stdio", "command": "npx", "shell": true}`,
		`{"type": "streamable-http", "url": "http://api.example/mcp"}`,
		`{"type": "streamable-http", "url": "https://u:p@api.example/mcp"}`,
		`{"type": "streamable-http", "url": "https://api.example/mcp", "headers": {"X-A": "1", "x-a": "2"}}`,
		`{"type": "websocket", "url": "wss://x"}`,
	} {
		dir := plugin(t, map[string]string{
			"plugin.json": manifest + "}",
			"mcp.json":    `{"$schema": "https://agent-plugins.org/schemas/1.0.0/mcp.schema.json", "mcpServers": {"s": ` + server + `}}`,
		})
		p, problems, err := Read(dir, "/data")
		if err != nil || len(p.MCP) > 0 || len(problems) != 1 {
			t.Errorf("%s: MCP %v, problems %v, err %v", server, p.MCP, problems, err)
		}
	}
	// A loopback server may go without TLS.
	dir := plugin(t, map[string]string{
		"plugin.json": manifest + "}",
		"mcp.json":    `{"$schema": "https://agent-plugins.org/schemas/1.0.0/mcp.schema.json", "mcpServers": {"s": {"type": "streamable-http", "url": "http://127.0.0.1:8080/mcp"}}}`,
	})
	if p, _, _ := Read(dir, "/data"); len(p.MCP) != 1 {
		t.Error("a loopback server was refused")
	}
}

// What a symlink leads to outside the plugin is not read.
func TestSymlinksStayInside(t *testing.T) {
	outside := plugin(t, map[string]string{"SKILL.md": "---\ndescription: outside\n---\n"})
	dir := plugin(t, map[string]string{
		"plugin.json":        manifest + "}",
		"skills/in/SKILL.md": "---\ndescription: in\n---\n",
		"real/SKILL.md":      "---\ndescription: linked\n---\n",
	})
	for link, target := range map[string]string{"skills/out": outside, "skills/linked": filepath.Join(dir, "real")} {
		if err := os.Symlink(target, filepath.Join(dir, link)); err != nil {
			t.Fatal(err)
		}
	}
	p, problems, err := Read(dir, "/data")
	if err != nil {
		t.Fatal(err)
	}
	var names []string
	for _, s := range p.Skills {
		names = append(names, s.Name)
	}
	slices.Sort(names)
	if !slices.Equal(names, []string{"in", "linked"}) || len(problems) != 1 {
		t.Errorf("skills %q, problems %v", names, problems)
	}
}

func TestParseSource(t *testing.T) {
	home, _ := os.UserHomeDir()
	for raw, want := range map[string]Source{
		"github.com/acme/tools":                       {URL: "https://github.com/acme/tools"},
		"github.com/acme/tools#v1.2.0":                {URL: "https://github.com/acme/tools", Ref: "v1.2.0"},
		"https://git.example.com/team/lint.git#main":  {URL: "https://git.example.com/team/lint.git", Ref: "main"},
		"git@github.com:acme/tools.git":               {URL: "git@github.com:acme/tools.git"},
		"ssh://git@host/acme/tools":                   {URL: "ssh://git@host/acme/tools"},
		"./tools/plugin":                              {Dir: "/base/tools/plugin"},
		"../plugin":                                   {Dir: "/plugin"},
		"/abs/plugin":                                 {Dir: "/abs/plugin"},
		"~/plugins/x":                                 {Dir: filepath.Join(home, "plugins/x")},
		"github.com/acme/plugins//plugins/tools#main": {URL: "https://github.com/acme/plugins", Path: "plugins/tools", Ref: "main"},
		"https://git.example.com/a/b.git//x#v1":       {URL: "https://git.example.com/a/b.git", Path: "x", Ref: "v1"},
		"git@github.com:acme/plugins.git//tools":      {URL: "git@github.com:acme/plugins.git", Path: "tools"},
		"https://github.com/acme/market/":             {URL: "https://github.com/acme/market"},
		"git.example.com:8443/team/lint#v2":           {URL: "https://git.example.com:8443/team/lint", Ref: "v2"},
	} {
		got, err := ParseSource(raw, "/base")
		if err != nil || got != want {
			t.Errorf("%s: %+v, %v", raw, got, err)
		}
		if again, err := ParseSource(got.String(), "/base"); got.Dir == "" && (err != nil || again != got) {
			t.Errorf("%s told as %s reads %+v, %v", raw, got, again, err)
		}
	}
	for _, raw := range []string{"acme-tools", "http://github.com/acme/tools", "ext::sh -c touch% /tmp/x", "github.com/acme/tools#-upload-pack=x", "file:///tmp/x", "",
		"github.com/acme/plugins//../x", "github.com/acme/plugins//", "github.com/acme/plugins//a/../b", "github.com/acme/plugins///abs",
		".codebot/plugins/kit", "plugins/kit/x", "-x/acme/tools"} {
		if _, err := ParseSource(raw, "/base"); err == nil {
			t.Errorf("%s parsed", raw)
		}
	}
}

// A repository's commits are cached a level under it, however its URL
// names it, apart from any other repository's.
func TestCachedKeepsSourcesApart(t *testing.T) {
	cached := func(raw string) string {
		s, err := ParseSource(raw, "/")
		if err != nil {
			t.Fatal(err)
		}
		return Cached(s, "/c", "abc")
	}
	tools := cached("github.com/acme/tools#v1")
	if rel, _ := filepath.Rel("/c", tools); !strings.HasPrefix(rel, "tools-") || filepath.Base(rel) != "abc" || strings.Count(rel, string(filepath.Separator)) != 1 {
		t.Errorf("tools at %s", rel)
	}
	if cached("git@github.com:acme/tools.git") != tools || cached("github.com/acme/tools//plugins/lint") != tools {
		t.Error("one repository is cached twice")
	}
	if cached("github.com/fork/tools") == tools {
		t.Error("two repositories share their cache")
	}
	if name := filepath.Base(filepath.Dir(cached("github.com/acme/.github"))); strings.HasPrefix(name, ".") {
		t.Errorf("a repository named with a dot is cached as a fetch under way: %s", name)
	}
}

// Fetch takes the ref's commit, or the commit given, into the cache,
// without its history.
func TestFetch(t *testing.T) {
	repo := t.TempDir()
	run := func(args ...string) string {
		cmd := exec.Command("git", append([]string{"-c", "user.name=t", "-c", "user.email=t@t"}, args...)...)
		cmd.Dir = repo
		out, err := cmd.CombinedOutput()
		if err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, out)
		}
		return strings.TrimSpace(string(out))
	}
	run("init", "-q", "-b", "main")
	write(t, filepath.Join(repo, "plugin.json"), manifest+"}")
	run("add", ".")
	run("commit", "-q", "-m", "v1")
	run("tag", "v1")
	v1 := run("rev-parse", "HEAD")
	write(t, filepath.Join(repo, "plugin.json"), manifest+`, "description": "two"}`)
	run("commit", "-qam", "v2")

	// The test's repository is local, which codebot's fetch refuses.
	t.Setenv("GIT_CONFIG_COUNT", "1")
	t.Setenv("GIT_CONFIG_KEY_0", "protocol.file.allow")
	t.Setenv("GIT_CONFIG_VALUE_0", "always")
	cache := t.TempDir()
	src := Source{URL: "file://" + repo, Ref: "v1"}
	commit, err := Fetch(t.Context(), src, cache, "")
	if err != nil {
		t.Fatal(err)
	}
	dir := Cached(src, cache, commit)
	if commit != v1 {
		t.Errorf("fetched %s, want %s", commit, v1)
	}
	if _, err := os.Stat(filepath.Join(dir, ".git")); err == nil {
		t.Error("the cache kept the history")
	}
	if p, _, err := Read(dir, "/data"); err != nil || p.Description != "" {
		t.Errorf("read %+v, %v", p, err)
	}
	if _, err := Fetch(t.Context(), Source{URL: src.URL, Ref: "nope"}, cache, ""); err == nil {
		t.Error("a missing ref fetched")
	}
	head := Source{URL: src.URL, Ref: "main"}
	if err := os.RemoveAll(Cached(head, cache, v1)); err != nil {
		t.Fatal(err)
	}
	if got, err := Fetch(t.Context(), head, cache, v1); err != nil || got != v1 {
		t.Errorf("fetched %s, %v; want %s, the commit given over the ref's", got, err, v1)
	}
	if _, err := Fetch(t.Context(), head, cache, strings.Repeat("0", 40)); err == nil {
		t.Error("a missing commit fetched")
	}
	entries, _ := os.ReadDir(cache)
	for _, e := range entries {
		if strings.HasPrefix(e.Name(), ".fetch-") {
			t.Errorf("a failed fetch left %s", e.Name())
		}
	}
}

// A plugin in a repository's directory is read from the commit fetched,
// which its directory may not lead out of.
func TestReadCachedStaysInTheRepository(t *testing.T) {
	cache := t.TempDir()
	src, err := ParseSource("github.com/acme/plugins//plugins/tools", "")
	if err != nil {
		t.Fatal(err)
	}
	repo := Cached(src, cache, "abc")
	write(t, filepath.Join(repo, "plugins", "tools", "plugin.json"), manifest+"}")
	if p, _, err := ReadCached(src, cache, "abc", "/data"); err != nil || p.Name != "acme-tools" {
		t.Fatalf("read %+v, %v", p, err)
	}
	outside := t.TempDir()
	write(t, filepath.Join(outside, "plugin.json"), manifest+"}")
	if err := os.Symlink(outside, filepath.Join(repo, "plugins", "away")); err != nil {
		t.Fatal(err)
	}
	away := src
	away.Path = "plugins/away"
	if _, _, err := ReadCached(away, cache, "abc", "/data"); err == nil {
		t.Error("read a plugin outside the repository")
	}
}

// codebot's namespace brings hooks and agents; another client's is not
// looked into.
func TestCodebotNamespace(t *testing.T) {
	dir := t.TempDir()
	write(t, filepath.Join(dir, "plugin.json"), manifest+`, "extensions": {
		"com.openai": {"hooks": "./hooks/hooks.json"},
		"io.github.voocel.codebot": {
			"hooks": {
				"PreToolUse": [
					{"type": "command", "command": "\"$PLUGIN_ROOT\"/bin/guard", "matcher": "bash"},
					{"type": "http", "url": "http://hooks.example/x"}
				],
				"Stop": [{"type": "command", "command": "true"}],
				"SessionStart": [{"type": "command", "command": "true", "env": {"PLUGIN_ROOT": "/x"}}]
			},
			"agents": "./agents"
		}
	}}`)
	write(t, filepath.Join(dir, "agents", "reviewer.md"), "---\ndescription: Reviews\n---\nReview the change.\n")
	write(t, filepath.Join(dir, "agents", "notes.txt"), "not an agent")
	outside := filepath.Join(t.TempDir(), "secret.md")
	write(t, outside, "---\ndescription: x\n---\nx\n")
	if err := os.Symlink(outside, filepath.Join(dir, "agents", "away.md")); err != nil {
		t.Fatal(err)
	}
	p, problems, err := Read(dir, "/data/acme-tools")
	if err != nil {
		t.Fatal(err)
	}
	if len(problems) != 4 {
		t.Errorf("problems %v: want the http, Stop, env and outside ones", problems)
	}
	hooks := p.Hooks["PreToolUse"]
	if len(p.Hooks) != 1 || len(hooks) != 1 || hooks[0].Env["PLUGIN_ROOT"] != p.Root || hooks[0].Env["PLUGIN_DATA"] != "/data/acme-tools" {
		t.Errorf("hooks %+v", p.Hooks)
	}
	if len(p.Agents) != 1 || p.Agents[0].Name != "reviewer" {
		t.Errorf("agents %+v", p.Agents)
	}

	write(t, filepath.Join(dir, "plugin.json"), manifest+`, "extensions": {"io.github.voocel.codebot": {"hook": {}}}}`)
	if p, problems, err = Read(dir, "/data"); err != nil || len(problems) != 1 || p.Hooks != nil {
		t.Errorf("a namespace out of format: %+v, %v, %v", p, problems, err)
	}
}

// A commit no longer held is marked, and removed two weeks after; one held
// again loses its mark, and a fetch under way is left alone.
// A plugin keeps its data across refs, apart from any other source's.
func TestDataDir(t *testing.T) {
	dir := func(raw string) string {
		s, err := ParseSource(raw, "/")
		if err != nil {
			t.Fatal(err)
		}
		return DataDir(s, "/d")
	}
	v1, v2 := dir("github.com/acme/tools#v1"), dir("github.com/acme/tools#v2")
	if v1 != v2 || filepath.Dir(v1) != filepath.FromSlash("/d") || !strings.HasPrefix(filepath.Base(v1), "tools-") {
		t.Errorf("refs: %s, %s", v1, v2)
	}
	if lint := dir("github.com/acme/tools//plugins/lint"); lint == v1 || !strings.HasPrefix(filepath.Base(lint), "lint-") {
		t.Errorf("a directory of the repository: %s", lint)
	}
	if fork := dir("github.com/fork/tools"); fork == v1 {
		t.Error("two repositories share their data")
	}
	if local := dir("/src/tools"); local == v1 || !strings.HasPrefix(filepath.Base(local), "tools-") {
		t.Errorf("a local plugin: %s", local)
	}
}

// The cache keeps the commits read within unusedAge, and removes the
// others, and the repositories left with none; a fetch under way stays.
func TestSweepCache(t *testing.T) {
	cache := t.TempDir()
	hex := strings.Repeat("d", 40)
	tools, _ := ParseSource("example.test/acme/tools", "")
	dotted, _ := ParseSource("example.test/acme/.github", "")
	hexed, _ := ParseSource("example.test/"+hex+"/kit", "")
	used, unused := Cached(tools, cache, strings.Repeat("a", 40)), Cached(tools, cache, strings.Repeat("b", 40))
	gone := Cached(dotted, cache, strings.Repeat("c", 40))
	kept := Cached(hexed, cache, strings.Repeat("e", 40))
	fetching := filepath.Join(cache, ".fetch-1")
	for _, d := range []string{used, unused, gone, kept, fetching} {
		if err := os.MkdirAll(d, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	now := time.Now()
	old := now.Add(-unusedAge - time.Hour)
	for _, d := range []string{unused, gone, fetching} {
		if err := os.Chtimes(d, old, old); err != nil {
			t.Fatal(err)
		}
	}
	if err := SweepCache(cache, now); err != nil {
		t.Fatal(err)
	}
	exists := func(p string) bool { _, err := os.Stat(p); return err == nil }
	if exists(unused) || exists(gone) || exists(filepath.Dir(gone)) {
		t.Error("kept commits no one read, or a repository left with none")
	}
	if !exists(used) || !exists(kept) || !exists(fetching) {
		t.Error("swept a commit read lately, or a fetch under way")
	}
	if err := SweepCache(filepath.Join(cache, "none"), now); err != nil {
		t.Errorf("no cache yet: %v", err)
	}
}

// Reading a commit marks it used.
func TestReadCachedMarksTheCommitUsed(t *testing.T) {
	cache := t.TempDir()
	src, _ := ParseSource("github.com/acme/tools", "")
	repo := Cached(src, cache, "abc")
	write(t, filepath.Join(repo, "plugin.json"), manifest+"}")
	old := time.Now().Add(-unusedAge - time.Hour)
	if err := os.Chtimes(repo, old, old); err != nil {
		t.Fatal(err)
	}
	if _, _, err := ReadCached(src, cache, "abc", "/data"); err != nil {
		t.Fatal(err)
	}
	if err := SweepCache(cache, time.Now()); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(repo); err != nil {
		t.Errorf("swept the commit just read: %v", err)
	}
}
