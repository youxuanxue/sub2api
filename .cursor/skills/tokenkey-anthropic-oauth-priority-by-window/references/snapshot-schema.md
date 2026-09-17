## 7. 附录 B：snapshot JSON 形状

```json
{
  "version": 1,
  "captured_at": "2026-05-21T08:00:00Z",
  "edges": {
    "uk1": {
      "deployable": true,
      "instance_id": "mi-0abc...",
      "region": "eu-west-2",
      "platform": "lightsail",
      "domain": "api-uk1.tokenkey.dev",
      "oauth_accounts": [
        {
          "id": 123,
          "name": "en-ld-ls-16-1-b",
          "platform": "anthropic",
          "type": "oauth",
          "status": "active",
          "priority": 20,
          "concurrency": 2,
          "stability_tier": "l2",
          "session_window_end": "2026-05-21T10:00:00Z",
          "session_window_utilization": 0.42,
          "passive_usage_7d_utilization": 0.18,
          "passive_usage_7d_reset": 1769404154,
          "passive_usage_sampled_at": "2026-05-21T07:55:13Z"
        }
      ]
    },
    "fra1": { "deployable": false, "skipped_reason": "edge fra1 is planned; pass --allow-planned to include" }
  }
}
```
