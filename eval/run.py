#!/usr/bin/env python3
"""P7 regression eval harness for orch. Must run in a Herdr Head pane (it drives `orch`).

  python3 eval/run.py selfcheck          # verify fixtures (bug lines, hidden tests vs reference)
  python3 eval/run.py run [--only review|parallel|delegate] [--limit N]
  python3 eval/run.py report <dir>       # re-render a report from a finished eval directory

Experiments
  review:   each seeded-bug module is reviewed twice. The nominal author is an OpenAI model;
            same-family reviewer = codex-high-m (OpenAI), cross-family reviewer = claude-medium-m (Anthropic).
            Detected = a reported BUG_LINES entry within ±1 of a planted bug line.
  parallel: three independent functions per task. single = one codex-high-l worker writes all three;
            parallel = three codex-high-l workers, one function each. Judged by hidden unit tests.
Review and parallel experiments run as one orch DAG, so row caps, fallback and pane cleanup are orch's own.

  delegate: A proxies the top-tier Head with claude-high-m via orch ask/do and charges
            all reported usage to Head. B sends read-only/research tasks to
            codex-medium-h, writing tasks to codex-high-l or codex-high-m via ask/do.
            B charges worker usage separately; Head tokens are estimated from
            instruction and printed orch JSON report characters / 4 plus a short
            review turn. A's usage and B's Head estimate are context-growth proxies.
            Weighted total uses one relative per-key cost table in delegate.py;
            these are planning assumptions, not provider prices. Hidden judges
            score both arms, with failed B judgments marked for Head rework.
"""
import datetime
import json
import os
import re
import shutil
import signal
import subprocess
import sys
import textwrap

sys.path.insert(0, os.path.dirname(os.path.abspath(__file__)))
import tasks  # noqa: E402
import reference  # noqa: E402
import delegate  # noqa: E402

ORCH = shutil.which("orch") or os.path.expanduser("~/.local/bin/orch")
REVIEWERS = {"same": "codex-high-m", "cross": "claude-medium-m"}
BUG_LINES_RE = re.compile(r"BUG_LINES:\s*([0-9,\s]*)")


def sh(*args, cwd=None, check=True, env=None):
    res = subprocess.run(args, cwd=cwd, text=True, capture_output=True, env=env)
    if check and res.returncode != 0:
        raise SystemExit(f"{' '.join(args)} failed ({res.returncode}): {res.stderr.strip() or res.stdout.strip()}")
    return res


def git_workspace(path, files):
    os.makedirs(path, exist_ok=True)
    for name, body in files.items():
        with open(os.path.join(path, name), "w") as f:
            f.write(body)
    for args in (["init", "-q"], ["config", "user.email", "eval@orch"], ["config", "user.name", "orch-eval"],
                 ["add", "-A"], ["commit", "-q", "-m", "fixture"]):
        sh("git", "-C", path, *args)
    return sh("git", "-C", path, "rev-parse", "HEAD").stdout.strip()


def fill_contract(path, goal, inputs, owned, perms, outputs, base, check):
    s = open(path).read()
    s = s.replace("<one or two sentences>", goal)
    s = s.replace("- References: <paths, URLs, earlier result files>", inputs)
    s = s.replace("- <what not to do>", "- Add unspecified features or edit files outside the owned scope")
    s = s.replace("- base commit: <hash or none>   · worktree: <path or none>",
                  f"- base commit: {base}   · worktree: none")
    s = s.replace("- Interface assumptions: <signatures and schemas shared with other pieces>",
                  "- Interface assumptions: use the function signatures in the specification")
    s = s.replace("- Owned files (glob): none", f"- Owned files (glob): {owned}")
    s = s.replace("- Allowed evidence sources: <local paths, specific sites>. Do not search unrelated connectors",
                  "- Allowed evidence sources: files in the workspace only. No web or external connectors")
    s = s.replace("- Allowed: <test runs, local file writes ...>", perms)
    s = s.replace("- <files, diffs, documents, image paths>", outputs)
    s = s.replace("- Time: <minutes>", "- Time: 10 minutes")
    s = s.replace("- <commands to run, conditions to check>", check)
    with open(path, "w") as f:
        f.write(s)


def new_contract(run_id, name, model):
    out = sh(ORCH, "contract", "new", name, "--role", "recon", "--model", model, "--run", run_id).stdout.strip()
    return out


def numbered(code):
    return "\n".join(f"{i:>3}| {line}" for i, line in enumerate(code.splitlines(), 1))


def hidden_test(func):
    lines = ["import unittest", f"from {func['name']} import {func['name']}", "", "",
             f"class Hidden(unittest.TestCase):"]
    for i, (args, expected) in enumerate(func["cases"]):
        lines.append(f"    def test_{i}(self):")
        if isinstance(expected, str) and expected.startswith("raises:"):
            lines.append(f"        with self.assertRaises({expected.split(':', 1)[1]}):")
            lines.append(f"            {func['name']}(*{args!r})")
        else:
            lines.append(f"        self.assertEqual({func['name']}(*{args!r}), {expected!r})")
    lines += ["", "", "if __name__ == '__main__':", "    unittest.main()"]
    return "\n".join(lines) + "\n"


def spec_md(funcs):
    out = ["# Specification", "", "Implement each function in its own file (`<function>.py`) using only the standard library.", ""]
    for f in funcs:
        example = f["cases"][0]
        out.append(f"- `{f['name']}`: {f['spec']}  Example: `{f['name']}{example[0]!r}` → `{example[1]!r}`")
    return "\n".join(out) + "\n"


def selfcheck():
    ok = True
    for t in tasks.REVIEW:
        lines = t["code"].splitlines()
        for n in t["bug_lines"]:
            if n < 1 or n > len(lines):
                print(f"review {t['id']}: bug line {n} out of range")
                ok = False
        ns = {}
        exec(compile(t["code"], t["id"], "exec"), ns)
    for p in tasks.PARALLEL:
        for f in p["funcs"]:
            fn = getattr(reference, f["name"])
            for args, expected in f["cases"]:
                try:
                    got = fn(*args)
                    good = got == expected
                except Exception as err:  # noqa: BLE001
                    good = isinstance(expected, str) and expected == "raises:" + type(err).__name__
                    got = err
                if not good:
                    print(f"parallel {p['id']}.{f['name']}{args!r}: expected {expected!r}, reference {got!r}")
                    ok = False
    ok = delegate.selfcheck() and ok
    print("selfcheck", "passed" if ok else "failed", f"(review {len(tasks.REVIEW)}, parallel {len(tasks.PARALLEL)})")
    return ok


def build(root, only, limit, created):
    run_id = sh(ORCH, "run", "new", "--mode", "full", "--text",
                "P7 regression evaluation: cross-family versus same-family review, parallel versus single implementation").stdout.strip()
    created.append(run_id)  # from here on, main() ends this run on every exit path
    nodes, meta = [], {"run": run_id, "root": root, "review": [], "parallel": []}
    if only in (None, "review"):
        for t in tasks.REVIEW[:limit]:
            for variant, model in REVIEWERS.items():
                ws = os.path.join(root, "review", t["id"], variant)
                base = git_workspace(ws, {f"{t['id']}.py": t["code"], "SPEC.md": f"# Specification\n\n{t['spec']}\n"})
                name = f"rv-{t['id']}-{variant}"[:32]
                c = new_contract(run_id, name, model)
                fill_contract(
                    c,
                    goal=(f"Review `{t['id']}.py` against SPEC.md to find defects. Assume an OpenAI model wrote the code. "
                          "Report only defects supported by reproduction or the specification. On the final line of Deliverables in the result report, list defective line numbers "
                          "in the format `BUG_LINES: 3, 7` (or `BUG_LINES:` if none). Use the line numbers below."),
                    inputs=f"- Target: {ws}/{t['id']}.py, Specification: {ws}/SPEC.md\n- Numbered code:\n```\n{numbered(t['code'])}\n```",
                    owned="none",
                    perms="- Allowed: read files and reproduce with python3 (temporary files in /tmp). Do not edit target files",
                    outputs="- Defect list (line number, reproduction, severity) and final line `BUG_LINES: ...`",
                    base=base,
                    check="- Check for a BUG_LINES line",
                )
                nodes.append({"name": name, "model": model, "role": "recon", "purpose": "extract", "cwd": ws})
                meta["review"].append({"task": t["id"], "variant": variant, "model": model, "node": name,
                                       "bug_lines": t["bug_lines"]})
    if only in (None, "parallel"):
        for p in tasks.PARALLEL[:limit]:
            for variant in ("single", "parallel"):
                ws = os.path.join(root, "parallel", p["id"], variant)
                base = git_workspace(ws, {"SPEC.md": spec_md(p["funcs"])})
                parts = [p["funcs"]] if variant == "single" else [[f] for f in p["funcs"]]
                for k, funcs in enumerate(parts):
                    name = f"pp-{p['id']}-{variant[0]}{k}"
                    files = ", ".join(f"{f['name']}.py" for f in funcs)
                    c = new_contract(run_id, name, "codex-high-l")
                    fill_contract(
                        c,
                        goal=f"Implement the {', '.join(f['name'] for f in funcs)} functions in SPEC.md. One function per file: {files}.",
                        inputs=f"- Specification: {ws}/SPEC.md (functions in this part: {', '.join(f['name'] for f in funcs)})",
                        owned=files,
                        perms="- Allowed: write owned files and test directly with python3 (temporary files in /tmp)",
                        outputs=f"- {files} under {ws}/",
                        base=base,
                        check="- Run specification examples with python3 -c",
                    )
                    nodes.append({"name": name, "model": "codex-high-l", "role": "recon", "purpose": "mech-impl", "cwd": ws})
                meta["parallel"].append({"task": p["id"], "variant": variant, "workspace": ws,
                                         "nodes": [f"pp-{p['id']}-{variant[0]}{k}" for k in range(len(parts))],
                                         "funcs": [f["name"] for f in p["funcs"]]})
                hidden = os.path.join(root, "hidden", p["id"])
                os.makedirs(hidden, exist_ok=True)
                for f in p["funcs"]:
                    with open(os.path.join(hidden, f"test_{f['name']}.py"), "w") as fh:
                        fh.write(hidden_test(f))
    run_dir = os.path.expanduser(f"~/.orch/runs/{run_id}")
    with open(os.path.join(run_dir, "dag.yaml"), "w") as f:
        f.write("nodes:\n")
        for n in nodes:
            f.write("  - " + json.dumps(n, ensure_ascii=False) + "\n")
    with open(os.path.join(root, "meta.json"), "w") as f:
        json.dump(meta, f, ensure_ascii=False, indent=2)
    return meta


def parse_time(s):
    """Parse Go's RFC 3339 time (up to 9 fractional digits) into an aware datetime."""
    if not s or s.startswith("0001"):
        return None
    s = s.replace("Z", "+00:00")
    m = re.match(r"^(.*T\d\d:\d\d:\d\d)(\.\d+)?(.*)$", s)
    if m and m.group(2):
        s = m.group(1) + m.group(2)[:7] + m.group(3)
    return datetime.datetime.fromisoformat(s)


def evaluate(root):
    meta = json.load(open(os.path.join(root, "meta.json")))
    run_dir = os.path.expanduser(f"~/.orch/runs/{meta['run']}")
    state = json.load(open(os.path.join(run_dir, "dag-state.json")))["nodes"]
    rows_r, rows_p = [], []
    for m in meta["review"]:
        ns = state.get(m["node"], {})
        worker = ns.get("worker") or m["node"]
        text = open(os.path.join(run_dir, "results", worker + ".md")).read() if os.path.exists(
            os.path.join(run_dir, "results", worker + ".md")) else ""
        found = BUG_LINES_RE.findall(text)
        reported = [int(x) for x in re.findall(r"\d+", found[-1])] if found else None
        hit = reported is not None and any(abs(r - b) <= 1 for r in reported for b in m["bug_lines"])
        extra = [r for r in (reported or []) if all(abs(r - b) > 1 for b in m["bug_lines"])]
        rows_r.append({**m, "status": ns.get("status"), "reported": reported, "detected": hit, "extra": extra,
                       "fallback": worker != m["node"]})
    for m in meta["parallel"]:
        starts, ends = [], []
        statuses = []
        for n in m["nodes"]:
            ns = state.get(n, {})
            statuses.append(ns.get("status"))
            s, e = parse_time(ns.get("started")), parse_time(ns.get("ended"))
            if s and e:
                starts.append(s)
                ends.append(e)
        wall = (max(ends) - min(starts)).total_seconds() if starts else None
        worker_s = sum((e - s).total_seconds() for s, e in zip(starts, ends)) if starts else None
        hidden = os.path.join(root, "hidden", m["task"])
        env = dict(os.environ, PYTHONPATH=m["workspace"])
        passed = total = 0
        for f in m["funcs"]:
            res = subprocess.run([sys.executable, "-m", "unittest", "-q", f"test_{f}"], cwd=hidden, env=env,
                                 capture_output=True, text=True, timeout=60)
            n_tests = len(re.findall(r"def test_", open(os.path.join(hidden, f"test_{f}.py")).read()))
            total += n_tests
            if res.returncode == 0:
                passed += n_tests
            else:
                failed = len(re.findall(r"^(FAIL|ERROR):", res.stderr, re.M))
                passed += max(0, n_tests - failed) if "Ran" in res.stderr else 0
        rows_p.append({**m, "statuses": statuses, "wall_s": wall, "worker_s": worker_s, "passed": passed,
                       "total": total})
    return meta, rows_r, rows_p


def pct(a, b):
    return f"{(100.0 * a / b):.0f}% ({a}/{b})" if b else "—"


def report(root):
    meta, rows_r, rows_p = evaluate(root)
    out = [f"# P7 regression evaluation results", "", f"- Time: {datetime.datetime.now():%Y-%m-%d %H:%M}",
           f"- run: `{meta['run']}` · workspace: `{root}`", ""]
    if rows_r:
        out += ["## Review: same family (codex-high-m) versus different family (claude-medium-m), assumed OpenAI author", "",
                "| Task | same family detected | different family detected | same family false-positive lines | different family false-positive lines |", "|---|---|---|---|---|"]
        by = {}
        for r in rows_r:
            by.setdefault(r["task"], {})[r["variant"]] = r
        for task, v in by.items():
            s, c = v.get("same", {}), v.get("cross", {})
            mark = lambda r: ("○" if r.get("detected") else ("✕" if r.get("reported") is not None else f"no result ({r.get('status')})"))
            out.append(f"| {task} | {mark(s)} | {mark(c)} | {len(s.get('extra', []))} | {len(c.get('extra', []))} |")
        out.append("")
        for variant in ("same", "cross"):
            rs = [r for r in rows_r if r["variant"] == variant]
            out.append(f"- {variant} ({REVIEWERS[variant]}): detection rate {pct(sum(r['detected'] for r in rs), len(rs))}, "
                       f"total false-positive lines {sum(len(r['extra']) for r in rs)}, no result {sum(r['reported'] is None for r in rs)}")
        out.append("")
    if rows_p:
        out += ["## Implementation: single worker versus three parallel workers (codex-high-l)", "",
                "| Task | variant | hidden tests | wall time (s) | total worker time (s) | node status |", "|---|---|---|---|---|---|"]
        for r in rows_p:
            out.append(f"| {r['task']} | {r['variant']} | {r['passed']}/{r['total']} | "
                       f"{'—' if r['wall_s'] is None else round(r['wall_s'])} | "
                       f"{'—' if r['worker_s'] is None else round(r['worker_s'])} | {', '.join(str(s) for s in r['statuses'])} |")
        out.append("")
        for variant in ("single", "parallel"):
            rs = [r for r in rows_p if r["variant"] == variant]
            walls = [r["wall_s"] for r in rs if r["wall_s"] is not None]
            works = [r["worker_s"] for r in rs if r["worker_s"] is not None]
            out.append(f"- {variant}: tests passed {pct(sum(r['passed'] for r in rs), sum(r['total'] for r in rs))}, "
                       f"mean wall time {sum(walls) / len(walls):.0f}s, mean worker time {sum(works) / len(works):.0f}s"
                       if walls else f"- {variant}: no measurements")
        out.append("")
    out += ["## Interpretation notes", "",
            "- The sample is small (12 review pairs, 8 implementation pairs). Treat results as directional and use repeated measurements to assess variance.",
            "- The review author is hypothetical (seeded bugs). This does not measure self-preference bias on code actually written by an OpenAI model.",
            "- Reviewer strength (codex-high-m medium versus claude-medium-m medium) is confounded with family effects.",
            "- Parallel wall time may include waiting due to row limits. Total worker time approximates cost.", ""]
    text = "\n".join(out)
    path = os.path.join(root, "report.md")
    with open(path, "w") as f:
        f.write(text)
    with open(os.path.join(root, "rows.json"), "w") as f:
        json.dump({"review": rows_r, "parallel": rows_p}, f, ensure_ascii=False, indent=2, default=str)
    return path, text


def run_delegate(root, limit):
    """Run independent A/B fixtures through pane-free orch calls; persist each result."""
    selected = tasks.DELEGATE[:limit]
    meta = {"experiment": "delegate", "delegate": [], "tasks": [t["id"] for t in selected]}
    meta_path = os.path.join(root, "meta.json")
    with open(meta_path, "w") as f:
        json.dump(meta, f, indent=2)
    for task in selected:
        for arm in ("A", "B"):
            ws = os.path.join(root, "delegate", task["id"], arm)
            git_workspace(ws, task["files"])
            row = delegate.invoke(task, ws, arm, ORCH)
            meta["delegate"].append(row)
            with open(meta_path, "w") as f:
                json.dump(meta, f, indent=2)
            print(f"delegate {task['id']} {arm}: {row['quality_passed']}/{row['quality_total']} ({row['status']})", flush=True)
    return delegate.render(root, meta)


def main(argv):
    if len(argv) < 2 or argv[1] not in ("selfcheck", "run", "report"):
        print(__doc__)
        return 2
    if argv[1] == "selfcheck":
        return 0 if selfcheck() else 1
    if argv[1] == "report":
        root = argv[2]
        meta = json.load(open(os.path.join(root, "meta.json")))
        path, text = delegate.render(root, meta) if meta.get("experiment") == "delegate" else report(root)
        print(text)
        return 0
    only = argv[argv.index("--only") + 1] if "--only" in argv else None
    if only not in (None, "review", "parallel", "delegate"):
        print(f"unknown experiment: {only}", file=sys.stderr)
        return 2
    limit = int(argv[argv.index("--limit") + 1]) if "--limit" in argv else None
    if not selfcheck():
        return 1
    # Inside the repository (git-ignored) so workers' folder-trust prompts stay within the
    # Head's cwd, which is the only place orch answers them automatically.
    root = os.path.join(os.path.dirname(os.path.abspath(__file__)), "out", f"{datetime.datetime.now():%Y%m%d-%H%M%S}")
    os.makedirs(root)
    if only == "delegate":
        path, text = run_delegate(root, limit)
        print(text)
        print(f"report: {path}")
        return 0
    # Whatever happens (build failure, Ctrl-C, SIGTERM), a created run is ended on the way out.
    signal.signal(signal.SIGTERM, lambda *_: sys.exit(143))
    created = []
    try:
        meta = build(root, only, limit, created)
        print(f"eval directory: {root} · run {meta['run']}")
        res = subprocess.run([ORCH, "dag", "run", "--run", meta["run"], "--timeout", str(3 * 3600 * 1000)])
        print(f"DAG run exit code {res.returncode} (the report is created even if some nodes fail)")
    finally:
        for run_id in created:
            end = sh(ORCH, "run", "end", "--run", run_id, check=False)
            print(end.stdout.strip() or end.stderr.strip())
    path, text = report(root)
    print(text)
    print(f"report: {path}")
    return 0


if __name__ == "__main__":
    sys.exit(main(sys.argv))
