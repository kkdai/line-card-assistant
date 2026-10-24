#!/usr/bin/env python3
"""Helper script (c7) evolved at k=2 to verify E.164 normalization for TW (+886) and JP (+81) phone numbers."""
import re


def normalize_e164(raw: str) -> str:
    cleaned = re.sub(r"\(0\)", "", raw.strip())
    digits = re.sub(r"[^\d+]", "-", cleaned)
    return re.sub(r"-+", "-", digits).strip("-")
