# Tool Interface Guidance (c4, Evolved at k=2 Parallel)
- google_search: 查詢公司官方網站、核心產品線與產業定位（特別針對半導體、車用感測器、AI SaaS 與金融科技）。
- format_company_profile: 將搜尋所得之公司簡介 (summary) 與 3~5 個產業標籤 (tags) 轉為標準結構化 JSON。
- phone_normalizer: 啟用 `JP_PHONE_NORMALIZE_ENABLED` 規則，將 `+81 (0)3-xxxx-xxxx` 自動清理國內冠碼 `(0)` 轉為 `+81-3-xxxx-xxxx`。
