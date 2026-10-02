#!/usr/bin/env python3
"""Behavioral baseline for the refactor (docs/refactor-plan.md, phase 0).

Runs a fixed set of real tasks against a frozen snapshot of the repo
(BASE_COMMIT) with a given codebot binary in print/JSON mode, then records
per-task success, turns, tokens, cost, tool calls and compactions.

Every run is isolated: its own copy of the fixture and its own HOME, so the
real ~/.codebot is never touched. The provider's key is read from the
environment (<PROVIDER>_API_KEY, and <PROVIDER>_BASE_URL if set) and only
ever written into that throwaway HOME.

Usage:
  go build -o /tmp/codebot-phase0 ./cmd/codebot
  python3 scripts/baseline/run.py /tmp/codebot-phase0 phase0

BASELINE_PROVIDER and BASELINE_MODEL pick another model (default DeepSeek);
BASELINE_ONLY runs the listed tasks.
"""
import concurrent.futures
import glob
import json
import os
import re
import shutil
import subprocess
import sys
import tempfile
import time

BASE_COMMIT = "7938489"
PROVIDER = os.environ.get("BASELINE_PROVIDER", "deepseek")
MODEL = os.environ.get("BASELINE_MODEL", "deepseek-v4-flash")
TIMEOUT_S = 20 * 60
MAX_TURNS = 60

REPO = os.path.dirname(os.path.dirname(os.path.dirname(os.path.abspath(__file__))))
GO_ENV = {
    k: subprocess.check_output(["go", "env", k], text=True).strip()
    for k in ("GOMODCACHE", "GOCACHE", "GOPATH")
}


def sh(cmd, cwd, env=None, check=True):
    return subprocess.run(cmd, cwd=cwd, env=env, shell=True, check=check,
                          capture_output=True, text=True)


def git_commit(cwd, msg):
    sh("git add -A && git -c user.name=baseline -c user.email=baseline@local "
       f"commit -qm '{msg}'", cwd)


# --- tasks -----------------------------------------------------------------

def setup_bugfix(repo):
    path = os.path.join(repo, "internal/approval/bash_classify.go")
    src = open(path).read()
    buggy = src.replace("if ch == '\\\\' && !inSingle {", "if ch == '\\\\' && inSingle {", 1)
    assert buggy != src, "bug injection anchor not found"
    open(path, "w").write(buggy)


def verify_bugfix(repo, _m, env):
    r = sh("go test ./internal/approval/", repo, env=env, check=False)
    changed = sh("git diff --name-only", repo).stdout.split()
    tests_touched = any(p.endswith("_test.go") for p in changed)
    return r.returncode == 0 and not tests_touched, (
        f"go test rc={r.returncode}, test files touched={tests_touched}")


HUMAN_BYTES_TEST = '''package storage

import "testing"

func TestBaselineHumanBytes(t *testing.T) {
	cases := map[int64]string{
		0: "0B", 1023: "1023B", 1024: "1.0KiB", 1536: "1.5KiB",
		1048576: "1.0MiB", 5 * 1024 * 1024 * 1024: "5.0GiB",
		3 * 1024 * 1024 * 1024 * 1024: "3.0TiB",
	}
	for n, want := range cases {
		if got := HumanBytes(n); got != want {
			t.Errorf("HumanBytes(%d) = %q, want %q", n, got, want)
		}
	}
}
'''


def verify_feature(repo, _m, env):
    test_path = os.path.join(repo, "internal/storage/zz_baseline_humanbytes_test.go")
    open(test_path, "w").write(HUMAN_BYTES_TEST)
    r = sh("go test -run 'TestBaselineHumanBytes' ./internal/storage/", repo,
           env=env, check=False)
    return r.returncode == 0, f"hidden test rc={r.returncode}: {r.stdout[-300:]}{r.stderr[-300:]}"


def verify_rename(repo, _m, env):
    b = sh("go build ./... && go vet ./internal/diag/", repo, env=env, check=False)
    old = sh(r"grep -rnw 'ErrToolInput' --include='*.go' . || true", repo).stdout.strip()
    new = sh(r"grep -rnw 'ErrInvalidToolInput' --include='*.go' . | wc -l", repo).stdout.strip()
    ok = b.returncode == 0 and old == "" and int(new) >= 9
    return ok, f"build rc={b.returncode}, old refs={len(old.splitlines())}, new refs={new}"


QA_KINDS = ["header", "message", "model_change", "compaction", "reasoning_effort_change",
            "session_info", "plan_state", "goal_state", "llm_call"]


def verify_qa(repo, m, _env):
    answer = m["answer"]
    found = [k for k in QA_KINDS if re.search(r"\b" + re.escape(k) + r"\b", answer)]
    dirty = sh("git status --porcelain", repo).stdout.strip()
    ok = len(found) >= 8 and dirty == ""
    return ok, f"kinds mentioned {len(found)}/9, worktree clean={dirty == ''}"


DOC_TERMS = ["NewEngine", "ParseRuleSet", "isReadonlyBash", "FilesystemRoots",
             "WrapGate", "PreToolUse", "PlanModeAllowedTools", "CheckDangerousPath"]


def verify_doc(repo, _m, _env):
    path = os.path.join(repo, "docs/permissions.md")
    if not os.path.exists(path):
        return False, "docs/permissions.md missing"
    text = open(path).read()
    hits = [t for t in DOC_TERMS if t in text]
    lines = len(text.splitlines())
    return len(hits) >= 5 and lines >= 40, f"{lines} lines, terms {len(hits)}/{len(DOC_TERMS)}"


def verify_delegate(repo, m, _env):
    delegated = "subagent" in m["tools"]
    named = "isReadonlyBash" in m["answer"] and "bash_classify.go" in m["answer"]
    dirty = sh("git status --porcelain", repo).stdout.strip()
    return delegated and named and dirty == "", (
        f"delegated={delegated}, named={named}, worktree clean={dirty == ''}")


def verify_plan(repo, m, _env):
    dirty = sh("git status --porcelain", repo).stdout.strip()
    planned = len(m["answer"]) >= 300 and "internal/storage" in m["answer"]
    return dirty == "" and planned, f"answer {len(m['answer'])} chars, worktree clean={dirty == ''}"


TASKS = [
    {
        "id": "qa",
        "prompt": "只回答问题，不要修改任何文件。internal/storage 里的会话 JSONL 一共有哪些条目类型（列出 kind 的字符串值）？每种分别由哪个函数写入？",
        "verify": verify_qa,
    },
    {
        "id": "bugfix",
        "setup": setup_bugfix,
        "prompt": "go test ./internal/approval/ 有失败的用例。找到根因并修复实现代码，不要修改任何测试文件。修复后确认该包测试全部通过。",
        "verify": verify_bugfix,
    },
    {
        "id": "feature",
        "prompt": "在 internal/storage 包中新增导出函数 func HumanBytes(n int64) string：n < 1024 时返回形如 \"512B\" 的字符串；否则依次使用 KiB、MiB、GiB、TiB，选择使数值小于 1024 的最大单位（TiB 为上限），保留一位小数，例如 1536 → \"1.5KiB\"，1048576 → \"1.0MiB\"。为它补充单元测试，并确认测试通过。",
        "verify": verify_feature,
    },
    {
        "id": "rename",
        "prompt": "把 internal/diag 包中的 ErrToolInput 重命名为 ErrInvalidToolInput，更新仓库里所有引用，确保 go build ./... 通过。",
        "verify": verify_rename,
    },
    {
        "id": "longdoc",
        # Small window so this task exercises automatic compaction.
        "compact_window": 40000,
        "prompt": "逐个完整阅读 internal/approval 和 internal/hooks 目录下所有非测试的 .go 文件，然后写一份 docs/permissions.md，说明工具调用的权限判定流程：权限模式、规则解析、文件系统根目录、bash 只读分类、危险路径检查、hooks 如何参与判定。要求引用具体的函数名和类型名。",
        "verify": verify_doc,
    },
    {
        "id": "delegate",
        "prompt": "只回答问题，不要修改任何文件。用 subagent 工具把查找工作委托给 explore 子 agent：internal/approval 中判断 bash 命令是否只读的入口函数叫什么、在哪个文件？拿到结果后告诉我函数名和文件名。",
        "verify": verify_delegate,
    },
    {
        "id": "plan",
        # Nobody approves a write in print mode, so balanced mode lets the
        # agent read and answer with the plan, and change nothing.
        "mode": "balanced",
        "prompt": "我想给 internal/storage 增加按会话名称模糊搜索会话的能力。先阅读相关代码，然后给出实现方案：要改哪些文件、新增哪些函数、怎么测试。",
        "verify": verify_plan,
    },
]


# --- running ---------------------------------------------------------------

def go_env(home):
    env = dict(os.environ)
    env.update(GO_ENV)
    env["HOME"] = home
    return env


def write_settings(home, compact_window):
    env = PROVIDER.upper()
    provider = {
        "api_key": os.environ[env + "_API_KEY"],
        "models": [MODEL],
        "small_model": MODEL,
    }
    if os.environ.get(env + "_BASE_URL"):
        provider["base_url"] = os.environ[env + "_BASE_URL"]
    settings = {
        "provider": PROVIDER,
        "model": MODEL,
        "max_turns": MAX_TURNS,
        "providers": {PROVIDER: provider},
    }
    if compact_window:
        settings["compact_window"] = compact_window
    os.makedirs(os.path.join(home, ".codebot"), exist_ok=True)
    path = os.path.join(home, ".codebot", "settings.json")
    with open(path, "w") as f:
        json.dump(settings, f)
    os.chmod(path, 0o600)


def prepare_fixture(root):
    fixture = os.path.join(root, "fixture")
    os.makedirs(fixture)
    sh(f"git -C '{REPO}' archive {BASE_COMMIT} | tar -x -C '{fixture}'", REPO)
    # A first commit of the whole tree creates thousands of loose objects; an
    # automatic background gc would repack them while tasks copy the fixture.
    sh("git init -q && git config gc.auto 0", fixture)
    git_commit(fixture, "baseline fixture")
    return fixture


def parse_events(path):
    m = {"llm_calls": 0, "input": 0, "output": 0, "cache_read": 0, "cache_write": 0, "cost_usd": 0.0,
         "turns": 0, "tool_calls": 0, "tool_errors": 0, "runs": 0, "end_reason": "", "answer": "",
         "tools": []}
    last_text = ""
    for line in open(path):
        line = line.strip()
        if not line.startswith("{"):
            continue
        try:
            ev = json.loads(line)
        except ValueError:
            continue
        t = ev.get("type")
        if t == "message_end":
            msg = ev.get("message") or {}
            if msg.get("role") == "assistant":
                m["llm_calls"] += 1
                u = msg.get("usage") or {}
                for k in ("input", "output", "cache_read", "cache_write"):
                    m[k] += u.get(k, 0) or 0
                # Priced by codebot from the model list.
                m["cost_usd"] += (u.get("cost") or {}).get("total", 0)
                text = "".join(b.get("text", "") for b in (msg.get("blocks") or [])
                               if isinstance(b, dict) and b.get("type") == "text")
                if text.strip():
                    last_text = text
        elif t == "tool_start" and ev.get("name") not in m["tools"]:
            m["tools"].append(ev.get("name"))
        elif t == "run_end":
            m["runs"] += 1
            m["turns"] += ev.get("turns", 0)
            m["tool_calls"] += ev.get("tool_calls", 0)
            m["tool_errors"] += ev.get("failed_calls", 0)
            m["end_reason"] = ev.get("reason", "")
    m["answer"] = last_text
    m["cost_usd"] = round(m["cost_usd"], 4)
    return m


def count_compactions(home):
    n = 0
    for path in glob.glob(os.path.join(home, ".codebot", "projects", "*", "*.jsonl")):
        for line in open(path):
            if '"kind":"compaction"' in line:
                n += 1
    return n


def run_task(binary, fixture, root, task):
    tdir = os.path.join(root, task["id"])
    repo = os.path.join(tdir, "repo")
    home = os.path.join(tdir, "home")
    shutil.copytree(fixture, repo, symlinks=True)
    os.makedirs(home)
    if task.get("setup"):
        task["setup"](repo)
        git_commit(repo, "task setup")
    write_settings(home, task.get("compact_window"))

    events = os.path.join(tdir, "events.jsonl")
    stderr = os.path.join(tdir, "stderr.log")
    start = time.time()
    timed_out = False
    with open(events, "w") as out, open(stderr, "w") as err:
        try:
            proc = subprocess.run([binary, "-json", "-mode", task.get("mode", "trust"), task["prompt"]],
                                  cwd=repo, env=go_env(home), stdout=out, stderr=err,
                                  timeout=TIMEOUT_S)
            rc = proc.returncode
        except subprocess.TimeoutExpired:
            rc, timed_out = -1, True
    elapsed = round(time.time() - start, 1)

    m = parse_events(events)
    ok, detail = task["verify"](repo, m, go_env(home))
    # Drop the key from disk as soon as the task is done.
    os.remove(os.path.join(home, ".codebot", "settings.json"))
    m.pop("answer")
    m.pop("tools")
    m.update({"id": task["id"], "ok": ok, "detail": detail, "rc": rc,
              "timed_out": timed_out, "seconds": elapsed,
              "compactions": count_compactions(home)})
    return m


def render(label, binary, results):
    cols = ["id", "ok", "seconds", "llm_calls", "turns", "tool_calls", "tool_errors",
            "input", "output", "cache_read", "cost_usd", "compactions", "end_reason", "detail"]
    lines = [f"# Baseline `{label}`", "",
             f"- binary: `{binary}`", f"- fixture: `{BASE_COMMIT}`", f"- model: `{PROVIDER}/{MODEL}`",
             f"- passed: {sum(r['ok'] for r in results)}/{len(results)}",
             f"- total cost: ${sum(r['cost_usd'] for r in results):.4f}",
             f"- total input+output tokens: {sum(r['input'] + r['output'] for r in results)}", "",
             "| " + " | ".join(cols) + " |", "|" + "---|" * len(cols)]
    for r in results:
        lines.append("| " + " | ".join(" ".join(str(r[c]).split()) for c in cols) + " |")
    return "\n".join(lines) + "\n"


def main():
    if len(sys.argv) != 3:
        sys.exit(__doc__)
    binary, label = os.path.abspath(sys.argv[1]), sys.argv[2]
    if not os.environ.get(PROVIDER.upper() + "_API_KEY"):
        sys.exit(PROVIDER.upper() + "_API_KEY is not set")
    only = os.environ.get("BASELINE_ONLY", "")
    tasks = [t for t in TASKS if not only or t["id"] in only.split(",")]

    root = tempfile.mkdtemp(prefix=f"codebot-baseline-{label}-")
    fixture = prepare_fixture(root)
    print(f"work dir: {root}", flush=True)
    with concurrent.futures.ThreadPoolExecutor(max_workers=len(tasks)) as pool:
        futures = {pool.submit(run_task, binary, fixture, root, t): t["id"] for t in tasks}
        results = []
        for fut in concurrent.futures.as_completed(futures):
            r = fut.result()
            print(f"[{r['id']}] ok={r['ok']} {r['seconds']}s cost=${r['cost_usd']} {r['detail']}", flush=True)
            results.append(r)
    results.sort(key=lambda r: [t["id"] for t in TASKS].index(r["id"]))

    out_dir = os.path.join(REPO, "scripts", "baseline", "results")
    os.makedirs(out_dir, exist_ok=True)
    with open(os.path.join(out_dir, f"{label}.json"), "w") as f:
        json.dump(results, f, indent=2, ensure_ascii=False)
    with open(os.path.join(out_dir, f"{label}.md"), "w") as f:
        f.write(render(label, binary, results))
    print(render(label, binary, results))


if __name__ == "__main__":
    main()
