"""Custom trajectory and response quality evaluators for LINE Card Assistant (ADK 2.0 Go).

Used by `agents-cli eval grade` and `agents-cli eval run` via `eval/eval_config.yaml`.
"""

from __future__ import annotations

import json
import re
from typing import Any


def _as_dict(item: Any) -> dict[str, Any]:
    if isinstance(item, dict):
        return item
    if hasattr(item, "model_dump"):
        return item.model_dump(mode="json")
    if hasattr(item, "dict"):
        return item.dict()
    return {}


def _extract_response_text(data: dict[str, Any]) -> str:
    resp = data.get("response")
    if isinstance(resp, str):
        return resp
    if isinstance(resp, dict):
        parts = resp.get("parts") or []
        texts = [p.get("text", "") for p in parts if isinstance(p, dict) and p.get("text")]
        if texts:
            return "\n".join(texts)

    responses = data.get("responses") or []
    for r in responses:
        if isinstance(r, dict):
            content = r.get("content") or r.get("response") or r
            if isinstance(content, dict):
                parts = content.get("parts") or []
                texts = [p.get("text", "") for p in parts if isinstance(p, dict) and p.get("text")]
                if texts:
                    return "\n".join(texts)
    return ""


def _extract_prompt_text(data: dict[str, Any]) -> str:
    prompt = data.get("prompt")
    if isinstance(prompt, str):
        return prompt
    if isinstance(prompt, dict):
        parts = prompt.get("parts") or []
        texts = [p.get("text", "") for p in parts if isinstance(p, dict) and p.get("text")]
        return "\n".join(texts)
    return ""


def _extract_trace_events(data: dict[str, Any]) -> list[dict[str, Any]]:
    agent_data = data.get("agent_data") or {}
    turns = agent_data.get("turns") or []
    events: list[dict[str, Any]] = []
    for turn in turns:
        if isinstance(turn, dict):
            for ev in turn.get("events") or []:
                if isinstance(ev, dict):
                    events.append(ev)
    return events


def _extract_merged_state(events: list[dict[str, Any]]) -> dict[str, Any]:
    merged: dict[str, Any] = {}
    for ev in events:
        # Support both top-level `state_delta` (agents-cli trace format) and `actions.stateDelta`
        state_delta = ev.get("state_delta") or ev.get("stateDelta")
        if not isinstance(state_delta, dict):
            actions = ev.get("actions") or {}
            state_delta = actions.get("state_delta") or actions.get("stateDelta") or {}
        if isinstance(state_delta, dict):
            merged.update(state_delta)
    return merged


def _extract_executed_nodes(events: list[dict[str, Any]]) -> list[str]:
    nodes: list[str] = []
    for ev in events:
        content = ev.get("content") or {}
        for part in content.get("parts") or []:
            if not isinstance(part, dict):
                continue
            txt = part.get("text", "")
            m = re.match(r"^\[([A-Za-z0-9_]+)\]", txt)
            if m:
                nodes.append(m.group(1))
            fc = part.get("function_call") or part.get("functionCall")
            if isinstance(fc, dict) and fc.get("name") == "adk_request_input":
                nodes.append("HITLConfirmNode")
    return nodes


def evaluate_workflow_trajectory(instance: Any, **kwargs: Any) -> dict[str, Any]:
    """Verifies that the ADK 2.0 Go Graph Workflow executed the expected node sequence and state transitions."""
    data = _as_dict(instance)
    prompt_text = _extract_prompt_text(data)
    events = _extract_trace_events(data)
    merged_state = _extract_merged_state(events)
    executed_nodes = _extract_executed_nodes(events)
    props = data.get("custom_Properties") or data.get("custom_properties") or {}

    if not events:
        return {
            "score": 0.0,
            "explanation": "No SSE workflow trace events captured in agent_data.turns.",
        }

    input_type = merged_state.get("input_type", "")
    expected_route = props.get("expected_route", "")
    checks: list[tuple[str, bool]] = []

    if expected_route:
        checks.append((f"input_type == {expected_route}", input_type == expected_route))

    scenario = props.get("scenario", "")
    if not scenario:
        if "promotion_david_chen" in prompt_text:
            scenario = "duplicate_promotion_detection"
        elif "edit=" in prompt_text and "hitl_action=confirm" in prompt_text:
            scenario = "hitl_edit_and_confirm_loop"
        elif prompt_text.strip() == "ingest":
            scenario = "ingest_fanout_fanin_ocr"
        elif "Cloud Summit" in prompt_text:
            scenario = "search_hybrid_found"
        elif "新竹科學園區" in prompt_text or "AI Chip" in prompt_text:
            scenario = "search_broad_fallback"
        elif "action=draft_email" in prompt_text:
            scenario = "email_draft_icebreaker"

    if scenario in ("ingest_fanout_fanin_ocr", "duplicate_promotion_detection"):
        checks.extend(
            [
                ("VisionOCRNode produced draft_card", "draft_card" in merged_state),
                ("CleanAndDedupNode normalized clean_phone", "clean_phone" in merged_state),
                (
                    "CompanyEnrichNode enriched company info & tags",
                    "enriched_company_info" in merged_state and "enriched_tags" in merged_state,
                ),
                ("ContextMatchNode inferred met_context", "enriched_met_context" in merged_state),
                ("BuildPreviewNode generated flex_preview_json", "flex_preview_json" in merged_state),
                (
                    "HITLConfirmNode paused with active_interrupt_id",
                    bool(merged_state.get("active_interrupt_id")) or "HITLConfirmNode" in executed_nodes,
                ),
            ]
        )
    elif scenario == "hitl_edit_and_confirm_loop":
        draft_card = merged_state.get("draft_card") or {}
        checks.extend(
            [
                ("VisionOCR + Fan-Out/Fan-In executed", "clean_phone" in merged_state and "enriched_tags" in merged_state),
                ("HITLConfirmNode routed to ApplyEditNode", "edit_instruction" in merged_state and "ApplyEditNode" in executed_nodes),
                (
                    "ApplyEditNode updated title & met_context",
                    isinstance(draft_card, dict)
                    and draft_card.get("title") == "技術副總"
                    and draft_card.get("met_context") == "COMPUTEX 2026 AI 論壇",
                ),
                ("SaveCardNode persisted saved_card_id", bool(merged_state.get("saved_card_id"))),
                ("SaveCardNode generated draft_email_text", bool(merged_state.get("draft_email_text"))),
            ]
        )
    elif scenario == "search_hybrid_found":
        checks.extend(
            [
                ("QueryIntentNode parsed search_filter", "search_filter" in merged_state),
                (
                    "HybridSearchNode returned non-empty search_results",
                    "HybridSearchNode" in executed_nodes
                    and isinstance(merged_state.get("search_results"), list)
                    and len(merged_state.get("search_results", [])) > 0,
                ),
                ("FormatCarouselNode built flex_carousel_json", bool(merged_state.get("flex_carousel_json"))),
            ]
        )
    elif scenario == "search_broad_fallback":
        checks.extend(
            [
                ("QueryIntentNode parsed search_filter", "search_filter" in merged_state),
                (
                    "HybridSearchNode -> BroadSearchNode fallback executed",
                    "HybridSearchNode" in executed_nodes and "BroadSearchNode" in executed_nodes,
                ),
                (
                    "BroadSearchNode returned non-empty search_results",
                    isinstance(merged_state.get("search_results"), list)
                    and len(merged_state.get("search_results", [])) > 0,
                ),
                ("FormatCarouselNode built flex_carousel_json", bool(merged_state.get("flex_carousel_json"))),
            ]
        )
    elif scenario == "email_draft_icebreaker":
        checks.extend(
            [
                ("RouterNode extracted target_card_id", merged_state.get("target_card_id") == "card-seed-1"),
                ("EmailDraftNode generated draft_email_text", bool(merged_state.get("draft_email_text"))),
            ]
        )

    if not checks:
        return {"score": 1.0, "explanation": f"Captured {len(events)} workflow events."}

    passed = sum(1 for _, ok in checks if ok)
    score = round(passed / len(checks), 4)
    details = "; ".join(f"{'PASS' if ok else 'FAIL'}: {label}" for label, ok in checks)
    return {
        "score": score,
        "explanation": f"Scenario={scenario} ({passed}/{len(checks)} trajectory checks passed): {details}",
    }


def evaluate_ocr_and_enrichment_quality(instance: Any, **kwargs: Any) -> dict[str, Any]:
    """Verifies E.164 phone normalization, company enrichment, tags, and Flex JSON structural integrity."""
    data = _as_dict(instance)
    events = _extract_trace_events(data)
    merged_state = _extract_merged_state(events)
    response_text = _extract_response_text(data)

    if merged_state.get("input_type") != "ingest":
        return {
            "score": 1.0,
            "explanation": "Non-ingest case (not applicable for OCR/Enrichment check; passed).",
        }

    draft_card = merged_state.get("draft_card") or {}
    clean_phone = str(merged_state.get("clean_phone", ""))
    company_info = str(merged_state.get("enriched_company_info", ""))
    tags = merged_state.get("enriched_tags") or []
    flex_json_str = str(merged_state.get("flex_preview_json", ""))

    flex_valid = False
    if flex_json_str:
        try:
            parsed_flex = json.loads(flex_json_str)
            flex_valid = parsed_flex.get("type") == "bubble"
        except Exception:
            flex_valid = False

    checks = [
        ("E.164 phone normalization (+886-9xx-xxx-xxx)", clean_phone.startswith("+886-9") and len(clean_phone) == 16),
        ("DraftCard populated name & email", bool(draft_card.get("name")) and "@" in str(draft_card.get("email", ""))),
        ("CompanyEnrichNode populated company_info", len(company_info) >= 15),
        ("CompanyEnrichNode populated >= 3 industry tags", isinstance(tags, list) and len(tags) >= 3),
        ("BuildPreviewNode produced valid LINE Flex Bubble JSON", flex_valid),
        ("Final response is non-empty", len(response_text.strip()) > 0),
    ]

    passed = sum(1 for _, ok in checks if ok)
    score = round(passed / len(checks), 4)
    details = "; ".join(f"{'PASS' if ok else 'FAIL'}: {label}" for label, ok in checks)
    return {
        "score": score,
        "explanation": f"OCR & Enrichment checks ({passed}/{len(checks)}): {details}",
    }


def evaluate_duplicate_and_hitl_precision(instance: Any, **kwargs: Any) -> dict[str, Any]:
    """Verifies duplicate contact / promotion banner detection and HITL Edit/Confirm state accuracy."""
    data = _as_dict(instance)
    prompt_text = _extract_prompt_text(data)
    events = _extract_trace_events(data)
    merged_state = _extract_merged_state(events)
    response_text = _extract_response_text(data)

    if "promotion_david_chen" in prompt_text:
        flex_json = str(merged_state.get("flex_preview_json", ""))
        draft_card = merged_state.get("draft_card") or {}
        checks = [
            ("is_duplicate == True", merged_state.get("is_duplicate") is True),
            ("duplicate_old_title == 'VP of Engineering'", merged_state.get("duplicate_old_title") == "VP of Engineering"),
            ("duplicate_card_id == 'card-seed-2'", merged_state.get("duplicate_card_id") == "card-seed-2"),
            ("New title extracted as CTO", "CTO" in str(draft_card.get("title", ""))),
            ("Flex Bubble renders duplicate warning header", "發現重複名片" in flex_json),
            ("Flex Bubble renders promotion alert with old title", "職稱異動" in flex_json and "VP of Engineering" in flex_json),
        ]
        passed = sum(1 for _, ok in checks if ok)
        return {
            "score": round(passed / len(checks), 4),
            "explanation": f"Duplicate & Promotion checks ({passed}/{len(checks)}): "
            + "; ".join(f"{'PASS' if ok else 'FAIL'}: {label}" for label, ok in checks),
        }

    if "edit=" in prompt_text and "hitl_action=confirm" in prompt_text:
        draft_card = merged_state.get("draft_card") or {}
        checks = [
            ("Edited title == '技術副總'", draft_card.get("title") == "技術副總"),
            ("Edited met_context == 'COMPUTEX 2026 AI 論壇'", draft_card.get("met_context") == "COMPUTEX 2026 AI 論壇"),
            ("Card saved with saved_card_id", bool(merged_state.get("saved_card_id"))),
            ("Confirmation reply contains updated title", "技術副總" in response_text and "✅ 名片已成功儲存" in response_text),
            ("Confirmation reply includes icebreaker email draft", "破冰跟進信草稿" in response_text),
        ]
        passed = sum(1 for _, ok in checks if ok)
        return {
            "score": round(passed / len(checks), 4),
            "explanation": f"HITL Edit & Confirm checks ({passed}/{len(checks)}): "
            + "; ".join(f"{'PASS' if ok else 'FAIL'}: {label}" for label, ok in checks),
        }

    return {
        "score": 1.0,
        "explanation": "Standard case (no duplicate/edit override required; passed).",
    }


def evaluate_search_and_email_grounding(instance: Any, **kwargs: Any) -> dict[str, Any]:
    """Verifies Hybrid Search, Broad Search fallback, and personalized Follow-up Email grounding."""
    data = _as_dict(instance)
    prompt_text = _extract_prompt_text(data)
    events = _extract_trace_events(data)
    merged_state = _extract_merged_state(events)
    response_text = _extract_response_text(data)

    input_type = merged_state.get("input_type", "")
    if input_type == "search":
        carousel_str = str(merged_state.get("flex_carousel_json", ""))
        carousel_valid = False
        if carousel_str:
            try:
                parsed = json.loads(carousel_str)
                carousel_valid = parsed.get("type") == "carousel" and len(parsed.get("contents", [])) > 0
            except Exception:
                carousel_valid = False

        if "Cloud Summit" in prompt_text:
            checks = [
                ("Valid Flex Carousel JSON", carousel_valid),
                ("Matched 林雅婷 (Google Cloud Taiwan)", "林雅婷" in response_text and "Google Cloud Taiwan" in response_text),
                ("Matched 陳建宏 (Appier)", "陳建宏" in response_text and "Appier" in response_text),
            ]
        else:
            checks = [
                ("Valid Flex Carousel JSON via BroadSearch fallback", carousel_valid),
                ("Matched 張志豪 (TSMC)", "張志豪" in response_text and "台積電" in response_text),
            ]
        passed = sum(1 for _, ok in checks if ok)
        return {
            "score": round(passed / len(checks), 4),
            "explanation": f"Search Carousel grounding ({passed}/{len(checks)}): "
            + "; ".join(f"{'PASS' if ok else 'FAIL'}: {label}" for label, ok in checks),
        }

    if input_type == "email":
        checks = [
            ("Mentions recipient 林雅婷", "林雅婷" in response_text),
            ("Grounds on met_context (2026 Cloud Summit Taipei)", "2026 Cloud Summit Taipei" in response_text),
            ("Grounds on company (Google Cloud Taiwan)", "Google Cloud Taiwan" in response_text),
            ("Includes email subject line (主旨：)", "主旨：" in response_text),
        ]
        passed = sum(1 for _, ok in checks if ok)
        return {
            "score": round(passed / len(checks), 4),
            "explanation": f"Email Draft grounding ({passed}/{len(checks)}): "
            + "; ".join(f"{'PASS' if ok else 'FAIL'}: {label}" for label, ok in checks),
        }

    return {
        "score": 1.0,
        "explanation": "Ingest case (not applicable for Search/Email check; passed).",
    }
