# Guardrails & Format Invariants (c2, Evolved at k=2 Parallel)
- 所有 JSON 輸出節點（VisionOCRNode、CompanyEnrichNode、QueryIntentNode、ApplyEditNode）必須僅輸出合法 JSON，不得包裹未經請求的額外說明。
- 電話號碼須正規化為 E.164 國際格式：台灣門號轉為 `+886-9xx-xxx-xxx`，日本門號須移除國內長途冠碼 `(0)` 並轉為 `+81-x-xxxx-xxxx`（參照 `jp_card_ocr` 與 `tool_guidance.md`）。
- 複合句修改指令必須透過 `hitl_multi_edit` 一次更新所有受影響欄位（姓名、職稱、電話、相遇場合），並同步反映於後續 `executive_email_drafter` 產生的跟進信中。
- 搜尋與豐富化階段請搭配 `tw_semicon_search` 確保產業別標籤精準對應。
- 嚴禁捏造名片上未出現的電子郵件或電話號碼。
