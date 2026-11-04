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
        value = json.loads(output)
        return value if isinstance(value, dict) else {}
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
        if isinstance(value, bool) or isinstance(value, float):
            return None
        n = int(value) if value is not None else None
        return n if n is not None and n >= 0 else None
    except (TypeError, ValueError):
        return None


def _weighted(key, input_tokens, output_tokens):
    if input_tokens is None or output_tokens is None:
        return None
    a, b = COST[key]
    return round(a * input_tokens + b * output_tokens, 2)


def invoke(task, workspace, arm, orch, timeout_seconds=1800):
    key = HEAD_KEY if arm == "A" else worker_key(task)
    cmd = [orch, task["mode"], "--model", key, "--cwd", workspace, "--json"]
    if task["mode"] == "do":
        owned = [name for name in task["solution"] if name not in task["files"] or task["solution"][name] != task["files"][name]]
        cmd += ["--own", ",".join(owned)]
    if task["mode"] == "ask":
        cmd.append("--fresh")
    cmd.append(task["prompt"])
    start = time.monotonic()
    try:
        res = subprocess.run(cmd, capture_output=True, text=True, timeout=timeout_seconds)
    except subprocess.TimeoutExpired as err:
        res = subprocess.CompletedProcess(cmd, 124, err.stdout or "", err.stderr or "timeout")
        if isinstance(res.stdout, bytes): res.stdout = res.stdout.decode(errors="replace")
        if isinstance(res.stderr, bytes): res.stderr = res.stderr.decode(errors="replace")
    Path(workspace, "attempt.json").write_text(json.dumps({"exit_code": res.returncode, "stdout": res.stdout, "stderr": res.stderr}, indent=2))
    wall_ms = round((time.monotonic() - start) * 1000)
    data = _parse_json(res.stdout)
    usage = data.get("usage") or {}
    if not isinstance(usage, dict): usage = {}
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
        "model_id": data.get("model_id"),
        "status": status, "exit_code": res.returncode,
        "baseline": "top-tier-worker-proxy" if arm == "A" else "lower-key-worker",
        "head_input_tokens": None, "head_output_tokens": None,
        "head_usage_source": "unavailable",
        "estimated_head_input_tokens": head_in if arm == "B" else None,
        "estimated_head_output_tokens": head_out if arm == "B" else None,
        "proxy_input_tokens": worker_in if arm == "A" else None,
        "proxy_output_tokens": worker_out if arm == "A" else None,
        "head_context_growth_proxy": None,
        "worker_tokens": separate_worker, "weighted_total_cost": weighted,
        "weighted_cost_source": "planning-weights-and-estimated-head" if arm == "B" else "planning-weights-proxy",
        "quality_passed": passed, "quality_total": total,
        "rework": arm == "B" and (passed < total or status != "done"),
        "wall_ms": wall_ms, "worker_duration_ms": data.get("duration_ms"),
        "orch_task": data.get("task"),
        "orch_run": data.get("run"), "workspace": workspace,
        "error_excerpt": (res.stderr or res.stdout)[-300:] if status != "done" else "",
    }


def render(root, meta):
    # Legacy entry point uses the same honest coverage report as repeated runs.
    import experiment
    rows = meta.get("delegate", [])
    for i, row in enumerate(rows):
        row.setdefault("repetition", i // 2)
    summary = experiment.summarize(rows, len(rows))
    text = "# Top-tier worker proxy versus lower-key delegation\n\nActual Head usage is unavailable. Estimates are separate; no savings demonstrated.\n\n```json\n" + json.dumps(summary, indent=2) + "\n```\n"
    Path(root, "report.md").write_text(text)
    Path(root, "rows.json").write_text(json.dumps({"delegate": rows}, indent=2))
    return str(Path(root, "report.md")), text
