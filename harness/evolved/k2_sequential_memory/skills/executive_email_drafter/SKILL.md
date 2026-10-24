---
name: executive_email_drafter
description: Crafts executive-level Traditional Chinese follow-up emails incorporating exact executive titles (e.g., Partner & FinTech Lead) and dinner/summit occasions.
---
當 `EmailDraftNode` 或 `SaveCardNode` 為高階主管撰寫破冰跟進信時：
1. 稱謂必須完整包含收件人中英文姓名與完整職銜（例如 `王心怡 (Cindy Wang) Partner & FinTech Lead 您好：` 或 `王大銘 (Ming Wang) 研發副總 您好：`）。
2. 首段須明確引用名片上的 `met_context` 相遇場合（例如 `FinTech Taipei 晚宴` 或 `2026 Cloud Summit Taipei`）。
3. 若 `LongTermMEMORY.md` 包含 `FINTECH_COMPLIANCE_SUBJECT_TAG`，針對金控客戶須於主旨加上 `[FinTech 合作提案]` 並附註金融合規與個資保護聲明。
4. 結尾提出具體的 15 分鐘線上會議交流邀請。
