#!/usr/bin/env python3
"""Harness² BenchmarkAdapter for LINE AI Business Card Assistant (ADK 2.0 Go Graph Workflow).

Bridges Phase 4's evaluation dataset (`eval/datasets/harness2-challenge-dataset.json`)
and Phase 5's 13-node OpenTelemetry Spans (`workflow.node.*`) into the
`google-research/harness2` Two-Level Recursive Optimization loop (arXiv:2610.14224).
"""

from __future__ import annotations

from dataclasses import dataclass, field
import json
import os
from pathlib import Path
import subprocess
import tempfile
from typing import Any, Mapping, Sequence

try:
    # Use official google-research/harness2 types when installed in PYTHONPATH
    from harness2.adapters.base import BenchmarkAdapter, Task, TaskOutcome  # type: ignore
    from harness2.harness import Harness, OPENCODE_SURFACE  # type: ignore
except ImportError:
    @dataclass(frozen=True)
    class SurfaceSpec:
        always_on_files: tuple[str, ...] = (
            "systemprompt.md",
            "guardrails.md",
            "LongTermMEMORY.md",
            "tool_descriptions/tool_guidance.md",
        )
        always_on_max: int = 24_000

    OPENCODE_SURFACE = SurfaceSpec()

    @dataclass
    class Harness:
        files: dict[str, str] = field(default_factory=dict)
        surface: SurfaceSpec = OPENCODE_SURFACE

        @classmethod
        def from_directory(cls, root: Path, surface: SurfaceSpec = OPENCODE_SURFACE) -> "Harness":
            files: dict[str, str] = {}
            for path in sorted(root.rglob("*")):
                if path.is_dir() or path.name == "README.md":
                    continue
                rel = path.relative_to(root).as_posix()
                files[rel] = path.read_text(encoding="utf-8")
            return cls(files=files, surface=surface)

        def write_to_directory(self, root: Path) -> None:
            root.mkdir(parents=True, exist_ok=True)
            for rel, content in self.files.items():
                target = root / rel
                target.parent.mkdir(parents=True, exist_ok=True)
                target.write_text(content, encoding="utf-8")

        def always_on_bytes(self) -> int:
            return sum(len(self.files.get(f, "").encode("utf-8")) for f in self.surface.always_on_files)

    @dataclass(frozen=True)
    class Task:
        id: str
        prompt: str
        domain: str = "general"
        metadata: Mapping[str, Any] = field(default_factory=dict)

    @dataclass
    class TaskOutcome:
        task_id: str
        score: float
        deliverable: str
        trajectory: str
        error: str | None = None
        artifacts: dict[str, Any] = field(default_factory=dict)

    class BenchmarkAdapter:  # pragma: no cover - structural protocol fallback
        name: str = "line-card-assistant"


class LineCardAdapter(BenchmarkAdapter):
    """Adapts the Go ADK 2.0 LINE Card Assistant workflow to google-research/harness2.

    Key architectural guarantees:
    1. Zero-Leakage Barrier: `execute()` invokes the Go workflow engine with ONLY the user
       prompt and the candidate `c1..c8` Harness directory. Evaluation rubrics are never
       mounted into the execution workspace or prompt context.
    2. OpenTelemetry Behavioral Trajectory: `execute()` captures all 13 `workflow.node.*`
       spans via `tracetest.InMemoryExporter` and formats them into `[ACTION]` /
       `[OBSERVATION]` / `[ERROR]` entries in `trajectory.md` for Propose-Probe-Compose.
    3. Multi-Rubric Continuous Scoring: `evaluate()` grades the resulting `deliverable.json`
       and `answer.txt` against 4 structural & semantic rubrics per task (0.25 each).
    """

    name = "line-card-assistant"

    def __init__(
        self,
        repo_root: Path | None = None,
        dataset_path: Path | None = None,
        base_harness_dir: Path | None = None,
        runner_binary: Path | None = None,
    ) -> None:
        self.repo_root = (repo_root or Path(__file__).resolve().parent.parent).resolve()
        self.dataset_path = (
            dataset_path or (self.repo_root / "eval" / "datasets" / "harness2-challenge-dataset.json")
        ).resolve()
        self.base_harness_dir = (
            base_harness_dir or (self.repo_root / "harness" / "base")
        ).resolve()
        self.runner_binary = runner_binary
        self._tasks_cache: list[Task] | None = None
        self._rubrics_by_task: dict[str, list[dict[str, Any]]] = {}

    def _ensure_runner_binary(self) -> Path:
        if self.runner_binary and self.runner_binary.exists():
            return self.runner_binary
        bin_dir = self.repo_root / "bin"
        bin_dir.mkdir(parents=True, exist_ok=True)
        bin_path = bin_dir / "harness-runner"
        cmd = ["go", "build", "-o", str(bin_path), "./cmd/harness-runner"]
        subprocess.run(cmd, cwd=self.repo_root, check=True)
        self.runner_binary = bin_path
        return bin_path

    def get_base_harness(self) -> Harness:
        """Loads the initial c1..c8 Harness tree from `harness/base/`."""
        return Harness.from_directory(self.base_harness_dir, surface=OPENCODE_SURFACE)

    def load_harness_from_dir(self, directory: Path) -> Harness:
        """Loads any candidate or evolved c1..c8 Harness tree from disk."""
        return Harness.from_directory(directory.resolve(), surface=OPENCODE_SURFACE)

    def list_tasks(self, split: str = "all") -> Sequence[Task]:
        """Loads evaluation tasks from Phase 4/5 dataset while keeping rubrics private to the adapter."""
        if self._tasks_cache is not None and split == "all":
            return self._tasks_cache

        raw = json.loads(self.dataset_path.read_text(encoding="utf-8"))
        tasks: list[Task] = []
        for case in raw.get("eval_cases", []):
            task_id = case["eval_case_id"]
            domain = case.get("domain", "general")
            parts = case.get("prompt", {}).get("parts", [])
            prompt_text = "\n".join(p.get("text", "").strip() for p in parts if p.get("text", "").strip())
            self._rubrics_by_task[task_id] = list(case.get("rubrics", []))

            if split == "baseline" and not task_id.startswith("baseline_"):
                continue
            if split == "challenge" and not task_id.startswith("challenge_"):
                continue

            # Zero-Leakage Barrier: Task.metadata intentionally excludes ground-truth rubrics
            tasks.append(
                Task(
                    id=task_id,
                    prompt=prompt_text,
                    domain=domain,
                    metadata={"eval_set_id": raw.get("eval_set_id", ""), "domain": domain},
                )
            )

        if split == "all":
            self._tasks_cache = tasks
        return tasks

    def execute(self, harness: Harness, task: Task, run_dir: Path) -> TaskOutcome:
        """Materializes the candidate c1..c8 Harness, runs the Go Graph Workflow, and scores the deliverable."""
        run_dir = run_dir.resolve()
        run_dir.mkdir(parents=True, exist_ok=True)

        harness_stage_dir = run_dir / "candidate_harness"
        harness.write_to_directory(harness_stage_dir)

        runner_bin = self._ensure_runner_binary()
        cmd = [
            str(runner_bin),
            f"--dataset={self.dataset_path}",
            f"--task-id={task.id}",
            f"--harness-dir={harness_stage_dir}",
            f"--run-dir={run_dir}",
        ]
        env = os.environ.copy()
        env["DRY_RUN"] = "true"
        env["LINE_CARD_HARNESS_DIR"] = str(harness_stage_dir)

        proc = subprocess.run(
            cmd,
            cwd=self.repo_root,
            env=env,
            capture_output=True,
            text=True,
            check=False,
        )

        answer_path = run_dir / "answer.txt"
        traj_path = run_dir / "trajectory.md"
        deliverable_path = run_dir / "deliverable.json"

        deliverable = answer_path.read_text(encoding="utf-8") if answer_path.exists() else ""
        trajectory = traj_path.read_text(encoding="utf-8") if traj_path.exists() else ""
        error_msg = proc.stderr.strip() if proc.returncode != 0 else None

        score, rubric_report = self._evaluate_with_report(task, deliverable, deliverable_path)
        return TaskOutcome(
            task_id=task.id,
            score=score,
            deliverable=deliverable,
            trajectory=trajectory,
            error=error_msg,
            artifacts=rubric_report,
        )

    def evaluate(self, task: Task, deliverable: str) -> float:
        """Scores the agent's final deliverable in [0.0, 1.0] across the task's 4 rubrics."""
        score, _ = self._evaluate_with_report(task, deliverable, None)
        return score

    def _evaluate_with_report(
        self,
        task: Task,
        deliverable: str,
        deliverable_json_path: Path | None,
    ) -> tuple[float, dict[str, Any]]:
        if not self._rubrics_by_task:
            self.list_tasks("all")

        rubrics = self._rubrics_by_task.get(task.id, [])
        if not rubrics:
            return (1.0 if deliverable.strip() else 0.0), {"passed": 0, "total": 0, "details": []}

        executed_nodes: list[str] = []
        if deliverable_json_path and deliverable_json_path.exists():
            try:
                snap = json.loads(deliverable_json_path.read_text(encoding="utf-8"))
                executed_nodes = list(snap.get("executed_nodes", []))
            except json.JSONDecodeError:
                executed_nodes = []
        else:
            marker = "=== SESSION STATE SNAPSHOT ==="
            if marker in deliverable:
                raw_json = deliverable.split(marker, 1)[1].strip()
                try:
                    snap = json.loads(raw_json)
                    executed_nodes = list(snap.get("executed_nodes", []))
                except json.JSONDecodeError:
                    executed_nodes = []

        passed_count = 0
        details: list[dict[str, Any]] = []
        for rubric in rubrics:
            ok = True
            for req_node in rubric.get("required_nodes", []):
                if req_node not in executed_nodes:
                    ok = False
                    break
            if ok:
                for forbid_node in rubric.get("forbidden_nodes", []):
                    if forbid_node in executed_nodes:
                        ok = False
                        break
            if ok:
                for sub in rubric.get("required_substrings", []):
                    if sub not in deliverable:
                        ok = False
                        break
            if ok:
                for forbid_sub in rubric.get("forbidden_substrings", []):
                    if forbid_sub in deliverable:
                        ok = False
                        break

            if ok:
                passed_count += 1
            details.append(
                {
                    "id": rubric.get("id", ""),
                    "passed": ok,
                }
            )

        score = round(passed_count / len(rubrics), 4)
        return score, {
            "passed": passed_count,
            "total": len(rubrics),
            "details": details,
        }


if __name__ == "__main__":
    adapter = LineCardAdapter()
    base_harness = adapter.get_base_harness()
    tasks = adapter.list_tasks()
    print(f"Loaded {len(tasks)} tasks for adapter '{adapter.name}' (base always-on bytes: {base_harness.always_on_bytes()})")
    with tempfile.TemporaryDirectory(prefix="line-card-adapter-smoke-") as tmp:
        outcome = adapter.execute(base_harness, tasks[0], Path(tmp) / tasks[0].id)
        print(f"Smoke task '{outcome.task_id}' score: {outcome.score:.2f}")
