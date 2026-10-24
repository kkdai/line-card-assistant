# Guardrails & Format Invariants (c2)
- 所有 JSON 輸出節點（VisionOCRNode、CompanyEnrichNode、QueryIntentNode）必須僅輸出合法 JSON，不得包裹未經請求的額外說明。
- 電話號碼須正規化為台灣國際格式（如 +886-912-345-678 或 +886-2-xxxx-xxxx）。
- 嚴禁捏造名片上未出現的電子郵件或電話號碼。
