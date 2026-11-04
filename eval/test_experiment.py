import contextlib
import io
import json
import datetime
import os
import subprocess
import tempfile
import unittest
from pathlib import Path
from unittest.mock import patch

import delegate
import experiment
import tasks


class Experiments(unittest.TestCase):
    def test_failed_import_earns_no_partial_credit(self):
        self.assertEqual(experiment.hidden_passed(3, 1, "ERROR: test_x (unittest.loader._FailedTest.test_x)\nRan 1 test in 0.000s"), 0)
        self.assertEqual(experiment.hidden_passed(3, 1, "FAIL: test_1 (test_x.Hidden.test_1)\nRan 3 tests in 0.001s"), 2)
        self.assertEqual(experiment.hidden_passed(3, 0, "Ran 3 tests in 0.001s\nOK"), 3)
        self.assertEqual(experiment.hidden_passed(3, 0, ""), 0)
        import re
        import run
        fixture = experiment.duration_functions(1)[0]
        with tempfile.TemporaryDirectory() as tmp:
            Path(tmp, "test_missing.py").write_text(run.hidden_test(fixture))
            result = subprocess.run([run.sys.executable, "-B", "-m", "unittest", "-q", "test_missing"], cwd=tmp, capture_output=True, text=True)
        cases = len(fixture["cases"])
        legacy = max(0, cases - len(re.findall(r"^(FAIL|ERROR):", result.stderr, re.M)))
        self.assertGreater(legacy, 0)  # Reproduces the old inflated import-failure score.
        self.assertEqual(experiment.hidden_passed(cases, result.returncode, result.stderr), 0)

    def test_exception_keeps_expected_quality_denominator(self):
        with tempfile.TemporaryDirectory() as tmp, patch("experiment.duration_attempt", side_effect=RuntimeError("fixture")), contextlib.redirect_stdout(io.StringIO()):
            root = Path(tmp, "failed")
            experiment.main(["run", "--only", "duration", "--duration-bucket", "1", "--repetitions", "1", "--root", str(root)])
            expected = sum(len(f["cases"]) for f in experiment.duration_functions(1))
            results = [json.loads(p.read_text()) for p in root.glob("attempt-*/result.json")]
            self.assertEqual(len(results), 2)
            self.assertTrue(all(r["quality_total"] == expected and r["quality_passed"] == 0 for r in results))

    def test_seed_balance_and_boundaries(self):
        a = experiment.schedule(["x", "y"], 4, 42)
        self.assertEqual(a, experiment.schedule(["x", "y"], 4, 42))
        self.assertNotEqual(a, experiment.schedule(["x", "y"], 4, 7))
        for name in ("x", "y"):
            pairs = [a[i:i+2] for i in range(0, len(a), 2) if a[i]["task"] == name]
            self.assertEqual(sum(p[0]["arm"] == "A" for p in pairs), 2)
            self.assertTrue(all(set(p["arm"] for p in pair) == {"A", "B"} for pair in pairs))
        self.assertEqual([experiment.observed_bucket(n) for n in [59,60,299,300,899,900]],
                         ["<1m", "1-5m", "1-5m", "5-15m", "5-15m", ">=15m"])
        with self.assertRaises(ValueError): experiment.schedule(["x"], 0)

    def test_coverage_unmatched_failure(self):
        self.assertIsNone(delegate._tokens(-1))
        self.assertIsNone(delegate._tokens(True))
        self.assertIsNone(delegate._tokens(1.5))
        self.assertIsNone(delegate._tokens("bad"))
        self.assertEqual(delegate._tokens("0"), 0)
        self.assertEqual(delegate._parse_json("[]"), {})
        rows = [dict(task="x", repetition=0, arm="A", wall_ms=100, status="failed", quality_passed=0, quality_total=3,
                     head_input_tokens=None, head_output_tokens=None)]
        s = experiment.summarize(rows, 2)
        self.assertEqual(s["unmatched_pairs"], 1)
        self.assertEqual(s["missing_attempts"], 1)
        self.assertEqual(s["groups"]["delegate/A"]["n_missing_actual_head"], 1)
        self.assertFalse(s["savings_demonstrated"])

    def test_schedule_never_calls_models(self):
        with tempfile.TemporaryDirectory() as tmp, patch("subprocess.run", side_effect=AssertionError("model call")), contextlib.redirect_stdout(io.StringIO()):
            root = Path(tmp, "schedule")
            self.assertEqual(experiment.main(["run", "--dry-run", "--only", "duration", "--root", str(root)]), 0)
            data = json.loads((root / "schedule.json").read_text())
            self.assertEqual({r["target_minutes"] for r in data["rows"]}, {1,5,15})
            self.assertEqual(len(data["rows"]), 24)
            experiment.render(root)
            summary = json.loads((root / "summary.json").read_text())
            self.assertEqual(summary["missing_attempts"],24)
            for n, count in [(1,3),(5,15),(15,45)]:
                funcs = experiment.duration_functions(n)
                self.assertEqual(len(funcs), count)
                self.assertEqual(len({f["name"] for f in funcs}), count)

    def test_fresh_actual_vs_estimate_and_raw_attempt(self):
        task = next(t for t in tasks.DELEGATE if t["mode"] == "ask")
        import subprocess
        raw = json.dumps(dict(status="done", answer=task["solution"], usage=dict(input_tokens=0, output_tokens=2)))
        with tempfile.TemporaryDirectory() as tmp, patch("subprocess.run", return_value=subprocess.CompletedProcess([],0,raw,"")) as call:
            row = delegate.invoke(task, tmp, "B", "orch")
            self.assertIn("--fresh", call.call_args.args[0])
            self.assertEqual(row["worker_tokens"]["input"],0)
            self.assertIsNone(row["head_input_tokens"])
            self.assertIsNotNone(row["estimated_head_input_tokens"])
            self.assertTrue(Path(tmp,"attempt.json").exists())
        with tempfile.TemporaryDirectory() as tmp, patch("subprocess.run", side_effect=subprocess.TimeoutExpired([],1,output="partial")):
            row = delegate.invoke(task,tmp,"A","orch")
            self.assertEqual(row["exit_code"],124)
            self.assertIsNone(row["head_input_tokens"])
            self.assertEqual(json.loads(Path(tmp,"attempt.json").read_text())["stdout"],"partial")

    def test_duration_pipeline_without_live_provider(self):
        import run
        with tempfile.TemporaryDirectory() as tmp:
            root = Path(tmp, "attempt")
            root.mkdir()
            state_dir = Path(tmp, "home", "runs", "fake-run")
            state_dir.mkdir(parents=True)
            calls = []
            funcs = experiment.duration_functions(1)
            reference = Path(__file__).with_name("reference.py").read_text()
            def fake_sh(*args, **kwargs):
                calls.append(args)
                stdout = ""
                if args[1:3] == ("run", "new"): stdout = "fake-run"
                if args[1:3] == ("dag", "run"):
                    now = datetime.datetime.now(datetime.timezone.utc).isoformat()
                    experiment.persist(state_dir / "dag-state.json", {"nodes":{"batch0":{"started":now,"ended":now,"status":"done"}}})
                    experiment.persist(state_dir / "run.json", {"workers":[{"name":"batch0","model_id":"fixture-only"}]})
                    for f in funcs:
                        original = f["name"].rsplit("_",1)[0]
                        (root / "workspace" / (f["name"] + ".py")).write_text(reference + "\n" + f["name"] + " = " + original + "\n")
                return subprocess.CompletedProcess(args,0,stdout,"")
            def contract(*args, **kwargs):
                path = root / (args[1] + ".md")
                path.write_text("· stage: recon\n## Goal\n<one or two sentences>\n## Plan\n- <ordered steps placeholder>\n")
                return str(path)
            row = dict(task="1", repetition=0, arm="parallel", target_minutes=1)
            with patch.dict(os.environ, ORCH_HOME=str(Path(tmp,"home"))), patch.object(run,"sh",side_effect=fake_sh), patch.object(run,"new_contract",side_effect=contract):
                result = experiment.duration_attempt(root,row,"fake-orch",120)
            self.assertEqual(result["status"],"done")
            self.assertEqual(result["quality_passed"],result["quality_total"])
            self.assertEqual(result["n_complete_worker_timings"],1)
            self.assertTrue((root / "raw-attempt.json").exists())
            self.assertTrue((root / "cleanup.json").exists())
            self.assertTrue(any(a[1:3] == ("run","end") for a in calls))
            dag = (state_dir / "dag.yaml").read_text()
            self.assertEqual(dag.count('"worktree"'),3)


if __name__ == "__main__":
    unittest.main()
