# Long-Term Domain Memory (c3, Populated by Harness² Outer-Loop Experience Reflector)

## Cross-Iteration Experience Log (Iteration k=1 -> k=2)
1. **[Lesson 1 — Bilingual Corporate Hierarchy Preservation]**: 日本半導體與車用電子供應商（如 Sony Semiconductor Solutions）名片常將事業部（`車載イメージセンサ事業部`）與職稱（`統括部長`）並列，`VisionOCRNode` 與 `CompanyEnrichNode` 必須保留雙語對照並標註 `CMOS Sensor`、`Automotive` 等細粒度硬體標籤。
2. **[Lesson 2 — Taiwanese Tech Park Colloquial Alias Mapping]**: 台灣商務使用者習慣以「竹科半導體大廠」、「CoWoS 先進封裝」指稱 `台積電 (TSMC)`；若 `QueryIntentNode` 未展開別名，會導致 `HybridSearchNode` 為 0 筆並退回 `BroadSearchNode` 產生雜訊。
3. **[Lesson 3 — Multi-Clause HITL Regex Short-Circuit Prevention]**: 在 `ApplyEditNode` 中，部分子句（如電話與相遇場合）命中決定性 Regex 後若直接返回，會漏掉口語轉折子句（如「名字其實是…」、「職稱升為…」），必須強制交由 `hitl_multi_edit` 進行全欄位語意合併。
4. **[Lesson 4 — Financial Holding Compliance & PII Governance (`FINTECH_COMPLIANCE_SUBJECT_TAG`)]**: 針對金控與銀行高階主管（如國泰金控 `Cathay Financial Holding` 的 `Partner & FinTech Lead`），外部跟進信主旨必須加上 `FINTECH_COMPLIANCE_SUBJECT_TAG` 前綴 `主旨：[FinTech 合作提案]`，且內文須明確聲明「遵循金融合規與個資保護規範，不附載聯絡電話與地址等敏感個資」，確保符合金融業資安與個資稽核要求。
