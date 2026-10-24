---
name: hitl_multi_edit
description: Parses multi-clause colloquial HITL modification instructions (such as 名字其實是..., 職稱升為...) without short-circuiting after deterministic regex matches.
---
當 `ApplyEditNode` 收到包含逗號或口語轉折詞（如「其實是」、「升為」、「我們是在…認識的」）的多子句修改指令時：
1. 不可僅因電話或場合子句命中 Regex 就提前結束。
2. 必須同時解析姓名修正（例如將 `王大明` 更新為 `王大銘 (Ming Wang)`）、職稱晉升（例如將 `資深技術經理` 更新為 `研發副總`）、E.164 電話（`+886-988-777-666`）與相遇場合（`2026 Cloud Summit Taipei`），並回傳完整更新後的名片 JSON。
