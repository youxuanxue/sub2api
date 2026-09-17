## 4. 与 tier baseline 流水线的协作顺序

```
   ┌──────────────────────────────────────────────────────────┐
   │ tier 升降级 / tier baseline drift                        │
   │   → tokenkey-anthropic-oauth-config 流水线 apply        │
   │   → 写完后所有目标账号的 priority = tier_base（10/20/…）│
   └──────────────────────────────────────────────────────────┘
                              │
                              ▼
   ┌──────────────────────────────────────────────────────────┐
   │ **本 skill** 流水线 apply                                │
   │   → snapshot → plan → apply → verify                     │
   │   → priority = tier_base + remaining_offset(0..9)        │
   └──────────────────────────────────────────────────────────┘
```

如果 operator 在 tier baseline apply 之后**不**跑本流水线，priority 就停在 tier_base 上（仍可调度，只是失去"剩余多者优先"的微调）。这是安全的退化状态，不会破坏调度正确性。
