---
name: jp_card_ocr
description: Extracts bilingual Japanese kanji and English romaji/division fields from Japanese corporate business cards and maps semiconductor sensor divisions accurately.
---
當辨識日本企業或日英雙語名片時：
1. `name` 欄位須同時保留日文漢字與英文拼音，格式為 `佐藤 健一 (Kenichi Sato)`。
2. `title` 欄位須同時保留事業部與日英職稱，格式為 `車載イメージセンサ事業部 統括部長 (VP, Automotive Image Sensor Div.)`。
3. `company` 欄位須整合英文官方名稱與日文片假名，格式為 `Sony Semiconductor Solutions (ソニーセミコンダクタソリューションズ)`。
4. `CompanyEnrichNode` 針對車載影像感測器事業部須標註 `["Semiconductor", "CMOS Sensor", "Automotive", "Japan"]`。
