"""Fixture judging and reporting for the Head-direct/delegation experiment."""

import json
import os
import subprocess
import sys
import tempfile
import time
from pathlib import Path

import tasks


# Relative planning weights, not vendor prices: normalize the top-tier Head input
# to 1 and charge output more heavily. Keep all model assumptions in one table.
COST = {
    "claude-high-m": (1.0, 5.0),
    "codex-low-h": (0.25, 1.0),
    "codex-medium-h": (0.25, 1.0),  # Legacy Luna records.
    "codex-medium-m": (0.35, 1.4),
    "codex-high-l": (0.50, 2.0),
    "codex-high-m": (0.70, 2.8),
}
HEAD_KEY = "claude-high-m"
REVIEW_INPUT = 128
REVIEW_OUTPUT = 64
HEAD_SAVING_THRESHOLD = 0.50


def worker_key(task):
    if task["mode"] == "ask":
        return "codex-low-h"
    # These fixtures specify tiny single-file implementations/fixes and constrained drafts.
    if task["type"] in ("implementation", "build_fix", "refactor", "doc_draft"):
        return "codex-medium-m"
    return "codex-high-m" if task["type"] == "test_fix" else "codex-high-l"


def write_files(directory, files):
    os.makedirs(directory, exist_ok=True)
    for name, body in files.items():
        path = Path(directory, name)
        path.parent.mkdir(parents=True, exist_ok=True)
        path.write_text(body)


def judge(task, workspace, answer=""):
    """Return passed and total hidden checks; no checker files enter the worker repo."""
    checks = task["checks"]
    if task["mode"] == "ask":
        body = answer.casefold()
        return sum(phrase.casefold() in body for phrase in checks), len(checks)
    if task["type"] == "doc_draft":
        body = Path(workspace, "GUIDE.md").read_text().casefold()
        return sum(phrase.casefold() in body for phrase in checks), len(checks)
    script = r'''
import json, pathlib, re, subprocess, sys, tempfile
checks = json.loads(sys.argv[1])
original = json.loads(sys.argv[2])
try:
    import target
except Exception:
    print(json.dumps([False] * len(checks)))
    raise SystemExit(0)
def _raises(fn, kind, *args):
    try: fn(*args)
    except Exception as err: return isinstance(err, kind)
    return False
def _test_count():
    return len(re.findall(r'^\s*def test_', pathlib.Path('test_target.py').read_text(), re.M))
def _tests_pass():
    return subprocess.run([sys.executable, '-B', '-m', 'unittest', '-q', 'test_target'],
                          capture_output=True, timeout=15).returncode == 0
def _tests_reject_bug():
    with tempfile.TemporaryDirectory() as tmp:
        pathlib.Path(tmp, 'target.py').write_text(original)
        pathlib.Path(tmp, 'test_target.py').write_text(pathlib.Path('test_target.py').read_text())
        return subprocess.run([sys.executable, '-B', '-m', 'unittest', '-q', 'test_target'],
                              cwd=tmp, capture_output=True, timeout=15).returncode != 0
results = []
for check in checks:
    try: results.append(bool(eval(check)))
    except Exception: results.append(False)
print(json.dumps(results))
'''
    try:
        res = subprocess.run([sys.executable, "-B", "-c", script, json.dumps(checks),
                              json.dumps(task["files"].get("target.py", ""))],
                             cwd=workspace, capture_output=True, text=True, timeout=60)
        values = json.loads(res.stdout.strip()) if res.returncode == 0 else []
    except (subprocess.TimeoutExpired, ValueError):
        values = []
    return sum(value is True for value in values[:len(checks)]), len(checks)


def selfcheck():
    good = True
    types = {}
    for task in tasks.DELEGATE:
        types[task["type"]] = types.get(task["type"], 0) + 1
        with tempfile.TemporaryDirectory(prefix="delegate-check-") as tmp:
            write_files(tmp, task["files"])
            untouched = judge(task, tmp)
            if task["mode"] == "ask":
                solved = judge(task, tmp, task["solution"])
            else:
                write_files(tmp, task["solution"])
                solved = judge(task, tmp)
            if untouched[0] == untouched[1] or solved[0] != solved[1]:
                print(f"delegate {task['id']}: untouched {untouched}, reference {solved}")
                good = False
    if len(tasks.DELEGATE) != 18 or set(types.values()) != {3} or len(types) != 6:
        print(f"delegate task distribution invalid: {types}")
        good = False
    print("delegate selfcheck", "passed" if good else "failed", f"({len(tasks.DELEGATE)} fixtures, {types})")
    return good


def _parse_json(output):
    try:
        return json.loads(output)
    except ValueError:
        for line in reversed(output.splitlines()):
            try:
                value = json.loads(line)
                if isinstance(value, dict):
                    return value
            except ValueError:
                pass
    return {}


def _tokens(value):
    try:
        return int(value) if value is not None else None
    except (TypeError, ValueError):
        return None


def _weighted(key, input_tokens, output_tokens):
    if input_tokens is None or output_tokens is None:
        return None
    a, b = COST[key]
    return round(a * input_tokens + b * output_tokens, 2)


def invoke(task, workspace, arm, orch):
    key = HEAD_KEY if arm == "A" else worker_key(task)
    cmd = [orch, task["mode"], "--model", key, "--cwd", workspace, "--json"]
    if task["mode"] == "do":
        owned = [name for name in task["solution"] if name not in task["files"] or task["solution"][name] != task["files"][name]]
        cmd += ["--own", ",".join(owned)]
    cmd.append(task["prompt"])
    start = time.monotonic()
    res = subprocess.run(cmd, capture_output=True, text=True)
    wall_ms = round((time.monotonic() - start) * 1000)
    data = _parse_json(res.stdout)
    usage = data.get("usage") or {}
    worker_in, worker_out = _tokens(usage.get("input_tokens")), _tokens(usage.get("output_tokens"))
    answer = data.get("answer") or ""
    passed, total = judge(task, workspace, answer)
    status = data.get("status", "failed")
    if res.returncode != 0:
        status = "failed"
    if arm == "A":
        head_in, head_out = worker_in, worker_out
        context = None if worker_in is None or worker_out is None else worker_in + worker_out
        weighted = _weighted(key, worker_in, worker_out)
        separate_worker = {"input": 0, "output": 0}
    else:
        # Prompt is Head output; the printed capped orch JSON report is Head input.
        # A short review turn is charged to Head, even when judging is automatic.
        head_out = (len(task["prompt"]) + 3) // 4 + REVIEW_OUTPUT
        head_in = (len(res.stdout) + 3) // 4 + REVIEW_INPUT
        context = head_in + head_out
        work = _weighted(key, worker_in, worker_out)
        weighted = None if work is None else round(work + _weighted(HEAD_KEY, head_in, head_out), 2)
        separate_worker = {"input": worker_in, "output": worker_out}
    return {
        "task": task["id"], "type": task["type"], "arm": arm, "key": key,
        "status": status, "exit_code": res.returncode,
        "head_input_tokens": head_in, "head_output_tokens": head_out,
        "head_context_growth_proxy": context,
        "worker_tokens": separate_worker, "weighted_total_cost": weighted,
        "quality_passed": passed, "quality_total": total,
        "rework": arm == "B" and (passed < total or status != "done"),
        "wall_ms": wall_ms, "worker_duration_ms": data.get("duration_ms"),
        "orch_task": data.get("task"),
        "orch_run": data.get("run"), "workspace": workspace,
        "error_excerpt": (res.stderr or res.stdout)[-300:] if status != "done" else "",
    }


def render(root, meta):
    rows = meta.get("delegate", [])
    out = ["# Head direct versus lower-key delegation", "",
           "A uses claude-high-m; B uses codex-low-h for read-only work and Terra for scoped writing/fixes and Sol for larger planned implementation or test fixes.",
           "Head input/output for A come from CLI usage. B estimates Head prompt and printed report at 4 characters/token, plus 128 input and 64 output review tokens.",
           "A's context-growth proxy is its measured input plus output; B's is the estimated Head instruction, report and review tokens. These proxies are not actual context-window deltas.",
           "Weighted costs use relative input/output factors: " + ", ".join(f"{k}={v[0]}/{v[1]}" for k, v in COST.items()) + ". They are assumptions, not prices.", "",
           "| Type | Arm | Tasks | Quality | Rework | Head in/out | Head context proxy | Weighted cost | Wall (s) |",
           "|---|---|---:|---:|---:|---:|---:|---:|---:|"]
    by = {}
    for row in rows:
        by.setdefault((row["type"], row["arm"]), []).append(row)
    types = list(dict.fromkeys(task["type"] for task in tasks.DELEGATE))
    sums = {}
    for kind in types:
        for arm in ("A", "B"):
            rs = by.get((kind, arm), [])
            if not rs:
                continue
            passed, total = sum(r["quality_passed"] for r in rs), sum(r["quality_total"] for r in rs)
            h_in = sum(r["head_input_tokens"] for r in rs if r["head_input_tokens"] is not None)
            h_out = sum(r["head_output_tokens"] for r in rs if r["head_output_tokens"] is not None)
            complete = all(r["head_input_tokens"] is not None and r["head_output_tokens"] is not None and r["weighted_total_cost"] is not None for r in rs)
            head = h_in + h_out if complete else None
            context = sum(r["head_context_growth_proxy"] for r in rs) if complete else None
            cost = sum(r["weighted_total_cost"] for r in rs) if complete else None
            walls = [r["wall_ms"] for r in rs if isinstance(r["wall_ms"], (int, float))]
            wall = round(sum(walls) / 1000, 1) if len(walls) == len(rs) else None
            sums[kind, arm] = (passed, total, head, cost)
            fmt = lambda v: "—" if v is None else str(round(v, 1) if isinstance(v, float) else v)
            out.append(f"| {kind} | {arm} | {len(rs)} | {passed}/{total} | {sum(r['rework'] for r in rs)} | "
                       f"{f'{h_in}/{h_out}' if complete else '—'} | {fmt(context)} | {fmt(cost)} | {fmt(wall)} |")
    out += ["", "## Decision by type", "",
            "Delegate when B quality is at least A quality, no B task needs rework, and B reduces Head tokens by at least 50%. Otherwise raise the worker key for quality failures; choose direct when Head savings are smaller.", "",
            "| Type | Decision | Head-token reduction |", "|---|---|---:|"]
    for kind in types:
        a, b = sums.get((kind, "A")), sums.get((kind, "B"))
        if not a or not b or len(by.get((kind, "A"), [])) != 3 or len(by.get((kind, "B"), [])) != 3:
            decision, saving = "insufficient data", None
        elif a[2] is None or b[2] is None or not a[2]:
            decision, saving = "insufficient token usage", None
        else:
            saving = 1 - b[2] / a[2]
            quality_ok = b[0] / b[1] >= a[0] / a[1]
            rework = any(r["rework"] for r in by[(kind, "B")])
            decision = "raise worker key" if not quality_ok or rework else ("delegate" if saving >= HEAD_SAVING_THRESHOLD else "direct")
        out.append(f"| {kind} | {decision} | {'—' if saving is None else f'{saving:.0%}'} |")
    out += ["", "## Per-task measurements", "",
            "| Task | Type | Arm/key | Status | Quality | Rework | Head in/out | Worker in/out | Context proxy | Weighted cost | Wall (s) |",
            "|---|---|---|---|---:|---:|---:|---:|---:|---:|---:|"]
    for row in rows:
        fmt = lambda v: "—" if v is None else str(v)
        worker = row["worker_tokens"]
        out.append(f"| {row['task']} | {row['type']} | {row['arm']}/{row['key']} | {row['status']} | "
                   f"{row['quality_passed']}/{row['quality_total']} | {'yes' if row['rework'] else 'no'} | "
                   f"{fmt(row['head_input_tokens'])}/{fmt(row['head_output_tokens'])} | "
                   f"{fmt(worker['input'])}/{fmt(worker['output'])} | {fmt(row['head_context_growth_proxy'])} | "
                   f"{fmt(row['weighted_total_cost'])} | {row['wall_ms'] / 1000:.1f} |")
    out += ["", "Full machine-readable measurements are in `rows.json`.",
            "Missing CLI usage remains missing; weighted totals and decisions requiring it are withheld.", ""]
    text = "\n".join(out)
    Path(root, "report.md").write_text(text)
    Path(root, "rows.json").write_text(json.dumps({"delegate": rows}, indent=2))
    return str(Path(root, "report.md")), text
