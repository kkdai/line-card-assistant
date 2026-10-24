#!/usr/bin/env python3
"""Reproducible Harness² Two-Level Recursive Optimization Benchmark Runner.

Evaluates the 8-task (32-rubric) Open-World Challenge Dataset across four
Harness² configurations:
  1. base                 — Unmodified initial Harness (k=0)
  2. k1_single_step       — Single-step Task-level Propose-Probe-Compose (k=1)
  3. k2_parallel          — Multi-step Parallel Recursion without LongTermMEMORY.md (k=2)
  4. k2_sequential_memory — Multi-step Sequential Recursion + Outer-loop Experience Reflector (k=2)
"""

from __future__ import annotations

import json
from pathlib import Path
import tempfile
from typing import Any

from line_card_adapter import LineCardAdapter


CONFIGS = [
    ("base", "Base Harness (k=0)", "harness/base"),
    ("k1_single_step", "Single-Step Propose-Probe-Compose (k=1)", "harness/evolved/k1_single_step"),
    ("k2_parallel", "Multi-Step Parallel Recursion (k=2, No Memory)", "harness/evolved/k2_parallel"),
    (
        "k2_sequential_memory",
        "Multi-Step Sequential Recursion + LongTermMEMORY (k=2)",
        "harness/evolved/k2_sequential_memory",
    ),
]


def run_benchmark() -> dict[str, Any]:
    repo_root = Path(__file__).resolve().parent.parent
    adapter = LineCardAdapter(repo_root=repo_root)
    tasks = adapter.list_tasks("all")

    report: dict[str, Any] = {
        "adapter": adapter.name,
        "dataset": str(adapter.dataset_path.relative_to(repo_root)),
        "num_tasks": len(tasks),
        "num_rubrics": len(tasks) * 4,
        "always_on_max_bytes": 24000,
        "configurations": {},
    }

    with tempfile.TemporaryDirectory(prefix="harness2-line-card-exp-") as tmp_root:
        tmp_path = Path(tmp_root)
        for key, label, rel_dir in CONFIGS:
            h_dir = repo_root / rel_dir
            harness_obj = adapter.load_harness_from_dir(h_dir)
            task_scores: dict[str, float] = {}
            rubrics_passed = 0
            rubrics_total = 0
            sample_trajectories: dict[str, str] = {}

            for task in tasks:
                run_dir = tmp_path / key / task.id
                outcome = adapter.execute(harness_obj, task, run_dir)
                task_scores[task.id] = outcome.score
                rubrics_passed += int(outcome.artifacts.get("passed", 0))
                rubrics_total += int(outcome.artifacts.get("total", 4))
                if task.id in ("challenge_jp_bilingual_ingest", "challenge_colloquial_tw_search"):
                    sample_trajectories[task.id] = outcome.trajectory

            total_score = round(sum(task_scores.values()), 2)
            accuracy_pct = round((rubrics_passed / max(rubrics_total, 1)) * 100.0, 1)
            report["configurations"][key] = {
                "label": label,
                "harness_dir": rel_dir,
                "always_on_bytes": harness_obj.always_on_bytes(),
                "skills_count": len([p for p in harness_obj.files if p.startswith("skills/") and p.endswith("SKILL.md")]),
                "task_scores": task_scores,
                "total_task_score": total_score,
                "rubrics_passed": rubrics_passed,
                "rubrics_total": rubrics_total,
                "accuracy_pct": accuracy_pct,
                "sample_trajectories": sample_trajectories,
            }

    out_path = repo_root / "harness" / "experiment_report.json"
    out_path.write_text(json.dumps(report, ensure_ascii=False, indent=2) + "\n", encoding="utf-8")
    return report


def print_summary_table(report: dict[str, Any]) -> None:
    print("=" * 98)
    print("Google Research Harness² × LINE Card Assistant (ADK 2.0 Go) — Recursive Optimization Benchmark")
    print("=" * 98)
    header = f"{'Configuration':<48} | {'Rubrics':<9} | {'Task Score':<11} | {'Accuracy':<9} | {'c1..c4 Bytes':<12}"
    print(header)
    print("-" * 98)
    for key, cfg in report["configurations"].items():
        rubric_str = f"{cfg['rubrics_passed']}/{cfg['rubrics_total']}"
        score_str = f"{cfg['total_task_score']:.2f}/8.00"
        acc_str = f"{cfg['accuracy_pct']:.1f}%"
        bytes_str = f"{cfg['always_on_bytes']} / 24000"
        print(f"{cfg['label']:<48} | {rubric_str:<9} | {score_str:<11} | {acc_str:<9} | {bytes_str:<12}")
    print("-" * 98)

    print("\nPer-Task Score Breakdown:")
    task_ids = list(next(iter(report["configurations"].values()))["task_scores"].keys())
    row_fmt = f"{'Task ID':<42} | {'Base (k=0)':<10} | {'k=1 Step':<10} | {'k=2 Par':<10} | {'k=2 Seq+Mem':<11}"
    print(row_fmt)
    print("-" * 98)
    for tid in task_ids:
        b0 = report["configurations"]["base"]["task_scores"][tid]
        k1 = report["configurations"]["k1_single_step"]["task_scores"][tid]
        k2p = report["configurations"]["k2_parallel"]["task_scores"][tid]
        k2s = report["configurations"]["k2_sequential_memory"]["task_scores"][tid]
        print(f"{tid:<42} | {b0:<10.2f} | {k1:<10.2f} | {k2p:<10.2f} | {k2s:<11.2f}")
    print("=" * 98)


if __name__ == "__main__":
    result = run_benchmark()
    print_summary_table(result)
