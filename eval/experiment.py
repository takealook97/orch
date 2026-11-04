"""Repeatable schedules and durable attempts. No model runs in schedule/selftest mode."""
import argparse
import json
import random
import re
import statistics
import time
from pathlib import Path

import delegate
import tasks


def schedule(items, repetitions=4, seed=1, arms=("A", "B")):
    if repetitions < 1:
        raise ValueError("repetitions must be positive")
    rng = random.Random(seed)
    rows = []
    for item in items:
        first = rng.randrange(2)
        for repeat in range(repetitions):
            order = arms if (repeat + first) % 2 == 0 else arms[::-1]
            rows.extend(dict(task=item, repetition=repeat, arm=a) for a in order)
    # Shuffle pairs, preserving the counterbalanced within-pair ordering.
    pairs = [rows[i:i+2] for i in range(0, len(rows), 2)]
    rng.shuffle(pairs)
    return [r for pair in pairs for r in pair]


def observed_bucket(seconds):
    return "<1m" if seconds < 60 else "1-5m" if seconds < 300 else "5-15m" if seconds < 900 else ">=15m"


def hidden_passed(cases, exit_code, stderr):
    """Count only a complete, loaded suite; loader/import errors earn no credit."""
    count = re.search(r"Ran (\d+) tests?", stderr)
    if not count or int(count.group(1)) != cases or "_FailedTest" in stderr:
        return 0
    if exit_code == 0:
        return cases
    failed = len(set(re.findall(r"^(?:FAIL|ERROR): (\S+)", stderr, re.M)))
    return max(0, cases - failed) if failed else 0


def summarize(rows, expected):
    groups = {}
    for r in rows:
        key = str(r.get("target_minutes", r.get("type", "delegate"))) + "/" + r["arm"]
        g = groups.setdefault(key, [])
        g.append(r)
    result = {}
    for key, rs in groups.items():
        times = [r["wall_ms"]/1000 for r in rs]
        n = sum(r.get("head_input_tokens") is not None and r.get("head_output_tokens") is not None for r in rs)
        def has_worker(r):
            usage = r.get("worker_tokens") or {}
            return (r.get("proxy_input_tokens") is not None and r.get("proxy_output_tokens") is not None) if r["arm"] == "A" else (usage.get("input") is not None and usage.get("output") is not None)
        with_worker = sum(has_worker(r) for r in rs)
        result[key] = dict(n_total=len(rs), n_with_actual_head=n, n_missing_actual_head=len(rs)-n,
                           n_with_worker_or_proxy_usage=with_worker, n_missing_worker_or_proxy_usage=len(rs)-with_worker,
                           wall_mean_s=statistics.mean(times), wall_stdev_s=statistics.stdev(times) if len(times)>1 else None,
                           wall_min_s=min(times), wall_max_s=max(times),
                           quality_passed=sum(r["quality_passed"] for r in rs),
                           quality_total=sum(r["quality_total"] for r in rs),
                           failures=sum(r.get("status") != "done" for r in rs))
    paired = {}
    for r in rows:
        paired.setdefault((r["task"], r["repetition"]), set()).add(r["arm"])
    return dict(groups=result, scheduled_attempts=expected, recorded_attempts=len(rows),
                complete_pairs=sum(len(x)==2 for x in paired.values()),
                unmatched_pairs=sum(len(x)!=2 for x in paired.values()),
                missing_attempts=expected-len(rows), savings_demonstrated=False)


def persist(path, value):
    path = Path(path)
    tmp = path.with_suffix(path.suffix + ".tmp")
    tmp.write_text(json.dumps(value, indent=2))
    tmp.replace(path)


def render(root):
    root = Path(root)
    plan = json.loads((root / "schedule.json").read_text())
    rows = [json.loads(p.read_text()) for p in sorted(root.glob("attempt-*/result.json"))]
    summary = summarize(rows, len(plan["rows"]))
    persist(root / "summary.json", summary)
    persist(root / "rows.json", rows)
    (root / "report.md").write_text("# Observed experiment\n\nA is a top-tier worker proxy, never actual Head usage. Actual Head observations remain missing.\nNo savings demonstrated. Target minutes are workload labels; inspect observed timings and quality.\n\n```json\n" + json.dumps(summary, indent=2) + "\n```\n")
    return root / "report.md"


def duration_functions(minutes):
    # Increasing batches of real parsing/algorithm functions, never sleeps. Target
    # labels are hypotheses, not assertions about actual model completion time.
    all_funcs = [f for p in tasks.PARALLEL for f in p["funcs"]]
    count = {1: 3, 5: 15, 15: 45}[minutes]
    result = []
    for i in range(count):
        f = dict(all_funcs[i % len(all_funcs)])
        f["name"] = f["name"] + "_" + str(i)
        result.append(f)
    return result


def duration_attempt(root, row, orch, timeout_seconds=3600):
    # Reuse the established contract/DAG pipeline. Every model works in its own
    # checkout; single gets the full specification, parallel gets disjoint parts.
    import run
    minutes = row["target_minutes"]
    funcs = duration_functions(minutes)
    ws = Path(root, "workspace")
    ws.mkdir()
    base = run.git_workspace(str(ws), {"SPEC.md": run.spec_md(funcs), ".gitignore":"__pycache__/\n*.pyc\n"})
    started = time.monotonic()
    started_unix = time.time()
    run_id = run.sh(orch, "run", "new", "--mode", "full", "--text", "Duration workload comparison").stdout.strip()
    persist(Path(root, "run.json"), {"run": run_id, "started_unix": time.time()})
    parts = [funcs] if row["arm"] == "single" else [funcs[i::3] for i in range(3)]
    nodes = []
    try:
        for i, part in enumerate(parts):
            name = "batch" + str(i)
            c = run.new_contract(run_id, name, "codex-high-l", role="produce", orch=orch)
            files = ", ".join(f["name"] + ".py" for f in part)
            run.fill_contract(c, "Implement functions " + files + " from SPEC.md.",
                              "- Specification: " + str(ws / "SPEC.md"), files,
                              "- Allowed: write owned files, run local Python tests", "- " + files, base,
                              "- Exercise every specification example")
            contract = Path(c)
            body = contract.read_text().replace("· stage: recon", "· stage: produce")
            body = re.sub(r"(?m)^- <ordered steps.*$",
                          "1. Read the listed function specifications in SPEC.md.\n"
                          "2. Implement each owned function in its named module using only the standard library; preserve signatures and exception behavior.\n"
                          "3. Exercise every specified example with python3 -B; fix failures within owned files.\n"
                          "4. Write the result with commands and evidence, then add the final status.", body)
            contract.write_text(body.replace("- Time: 10 minutes", "- Time: %d minutes" % max(10, minutes*2)))
            nodes.append(dict(name=name, model="codex-high-l", role="produce", purpose="mech-impl", cwd=str(ws),
                              worktree=str(Path(root, "worktrees", name))))
        rd = Path(run.os.environ.get("ORCH_HOME", str(Path.home() / ".orch"))) / "runs" / run_id
        # Concurrent writers are isolated by orch's worktree admission. Specs are
        # committed above; each worker has exclusive module names.
        (rd / "dag.yaml").write_text("nodes:\n" + "".join("  - " + json.dumps(n) + "\n" for n in nodes))
        result = run.sh(orch, "dag", "run", "--run", run_id, "--timeout", str(timeout_seconds*1000), check=False)
        persist(Path(root, "raw-attempt.json"), dict(stdout=result.stdout, stderr=result.stderr, exit_code=result.returncode))
        # Retain raw scheduler/provider identity evidence, even for failed nodes.
        state = json.loads((rd / "dag-state.json").read_text())
        persist(Path(root, "raw-dag-state.json"), state)
        raw_run = json.loads((rd / "run.json").read_text())
        persist(Path(root, "worker-identities.json"),
                [{k:w.get(k) for k in ("name","key","model_id","started","closed","status")} for w in raw_run.get("workers",[])])
        starts = [run.parse_time(n.get("started")) for n in state["nodes"].values()]
        spans = [(run.parse_time(n.get("started")), run.parse_time(n.get("ended"))) for n in state["nodes"].values()]
        worker_s = sum((e-s).total_seconds() for s,e in spans if s and e)
        timing_complete = all(s and e for s,e in spans)
        first_start = min((s.timestamp() for s in starts if s), default=None)
        merged = run.sh(orch, "merge", "--run", run_id, check=False)
        persist(Path(root, "raw-merge.json"), dict(stdout=merged.stdout, stderr=merged.stderr, exit_code=merged.returncode))
        ended = time.monotonic()
        passed = total = 0
        # Keep generated code outside the harness process and cap each hidden run.
        hidden = Path(root, "hidden")
        hidden.mkdir()
        for f in funcs:
            total += len(f["cases"])
            test_name = "test_" + f["name"]
            (hidden / (test_name + ".py")).write_text(run.hidden_test(f))
            try:
                res = run.subprocess.run([run.sys.executable, "-B", "-m", "unittest", "-q", test_name],
                                         cwd=hidden, env=dict(run.os.environ, PYTHONPATH=str(ws)),
                                         capture_output=True, text=True, timeout=60)
                persist(hidden / (f["name"] + ".result.json"), dict(exit_code=res.returncode, stdout=res.stdout, stderr=res.stderr))
                passed += hidden_passed(len(f["cases"]), res.returncode, res.stderr)
            except run.subprocess.TimeoutExpired:
                persist(hidden / (f["name"] + ".result.json"), dict(status="timeout"))
        return dict(**row, wall_ms=round((ended-started)*1000), quality_passed=passed, quality_total=total,
                    status="done" if result.returncode == 0 and merged.returncode == 0 and passed == total else "failed",
                    head_input_tokens=None, head_output_tokens=None, head_usage_source="unavailable", run=run_id,
                    observed_worker_seconds=worker_s if timing_complete else None,
                    n_worker_timings=len(spans), n_complete_worker_timings=sum(bool(s and e) for s,e in spans),
                    setup_and_first_start_delay_s=None if first_start is None else max(0,first_start-started_unix),
                    observed_bucket=observed_bucket(ended-started))
    finally:
        end = run.sh(orch, "run", "end", "--run", run_id, check=False)
        persist(Path(root, "cleanup.json"), dict(stdout=end.stdout, stderr=end.stderr, exit_code=end.returncode))


def main(argv=None):
    p = argparse.ArgumentParser(description=__doc__)
    p.add_argument("command", choices=["schedule", "run"])
    p.add_argument("--only", choices=["delegate", "duration"], default="delegate")
    p.add_argument("--repetitions", type=int, default=4)
    p.add_argument("--seed", type=int, default=1)
    p.add_argument("--timeout-seconds", type=int, default=3600)
    p.add_argument("--limit", type=int)
    p.add_argument("--duration-bucket", type=int, choices=[1, 5, 15], action="append")
    p.add_argument("--dry-run", action="store_true")
    p.add_argument("--root", default="eval/out/experiment-" + str(time.time_ns()))
    p.add_argument("--orch", default="orch")
    a = p.parse_args(argv)
    if a.timeout_seconds < 1 or a.repetitions < 1 or a.limit is not None and a.limit < 1:
        p.error("timeout, repetitions and limit must be positive")
    selected = tasks.DELEGATE[:a.limit]
    if a.only == "delegate":
        rows = schedule([t["id"] for t in selected], a.repetitions, a.seed)
    else:
        rows = schedule([str(n) for n in (a.duration_bucket or [1, 5, 15])], a.repetitions, a.seed, ("single", "parallel"))
        for r in rows: r["target_minutes"] = int(r["task"])
    root = Path(a.root).resolve()
    root.mkdir(parents=True, exist_ok=False)
    persist(root / "schedule.json", dict(version=1, seed=a.seed, repetitions=a.repetitions, timeout_seconds=a.timeout_seconds, experiment=a.only, rows=rows))
    if a.command == "schedule" or a.dry_run:
        print(root / "schedule.json")
        return 0
    results = []
    for i, row in enumerate(rows):
        attempt = root / ("attempt-%04d" % i)
        attempt.mkdir()
        persist(attempt / "intent.json", row)
        start = time.monotonic()
        try:
            if a.only == "delegate":
                import run
                task = next(t for t in selected if t["id"] == row["task"])
                ws = attempt / "workspace"
                run.git_workspace(str(ws), task["files"])
                result = {**delegate.invoke(task, str(ws), row["arm"], a.orch, a.timeout_seconds), **row}
            else:
                result = duration_attempt(attempt, row, a.orch, a.timeout_seconds)
        except BaseException as e:
            expected_cases = (sum(len(f["cases"]) for f in duration_functions(row["target_minutes"]))
                              if a.only == "duration" else
                              len(next(t for t in selected if t["id"] == row["task"])["checks"]))
            result = dict(**row, status="interrupted" if isinstance(e, KeyboardInterrupt) else "failed", quality_passed=0,
                          quality_total=expected_cases, wall_ms=round((time.monotonic()-start)*1000), error_type=type(e).__name__,
                          head_input_tokens=None, head_output_tokens=None)
            persist(attempt / "result.json", result)
            results.append(result)
            persist(root / "summary.json", summarize(results, len(rows)))
            if isinstance(e, (KeyboardInterrupt, SystemExit)): raise
            continue
        persist(attempt / "result.json", result)
        results.append(result)
        persist(root / "summary.json", summarize(results, len(rows)))
    print(render(root))
    return 0


if __name__ == "__main__":
    raise SystemExit(main())
