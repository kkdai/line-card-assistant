---
name: tw_semicon_search
description: Expands Taiwanese semiconductor colloquialisms (竹科半導體大廠, CoWoS, 先進封裝, 晶圓代工) into canonical TSMC / 台積電 and Semiconductor search filters.
---
當使用者在自然語言搜尋中使用台灣科技業口語別稱時：
1. 若提及「竹科半導體大廠」、「晶圓代工龍頭」、「CoWoS」或「先進封裝」，請將 `company` 解析為 `"台積電"`，並將 `keywords` 設為 `["台積電", "TSMC"]`。
2. 同時將 `tag` 欄位設為 `"Semiconductor"`，讓 `HybridSearchNode` 直接精準命中，避免退回 `BroadSearchNode` 造成全庫雜訊。
