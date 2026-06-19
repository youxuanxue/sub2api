# Kiro 账号配置 · 极简操作指南

> 给运营：拿到一个 Kiro / AWS CodeWhisperer 授权，在 TokenKey 后台配成可用账号。
> 全程在你自己的 Mac 上操作。

## 4 步搞定

**1. 装 Kiro 并登录**
- 装：`brew install --cask kiro`（或官网 <https://kiro.dev> 下载）
- 开：`open -a Kiro`
- 登录选 **组织 / 公司 SSO**，填起始地址（例 `https://d-906671b2ce.awsapps.com/start`），浏览器点 **批准**。

**2. 提取凭证**（终端粘贴运行，会打印一段 JSON）

```bash
python3 - <<'PY'
import json, glob, os
c = os.path.expanduser("~/.aws/sso/cache")
t = json.load(open(os.path.join(c, "kiro-auth-token.json")))
auth = "social" if str(t.get("authMethod","")).lower()=="social" else "idc"
out = {"access_token": t["accessToken"], "refresh_token": t["refreshToken"],
       "region": t.get("region","us-east-1"), "auth_method": auth}
if auth == "idc":
    reg = next(json.load(open(f)) for f in glob.glob(c+"/*.json")
               if not f.endswith("kiro-auth-token.json") and "clientSecret" in json.load(open(f)))
    out["client_id"], out["client_secret"] = reg["clientId"], reg["clientSecret"]
print(json.dumps(out, ensure_ascii=False, indent=2))
PY
```

> 这几段是敏感凭证（等于账号密码）：只在后台输入框粘贴，不发群、不存文件、不截图。

**3. 后台新建账号**
账号管理 → 新建 → 平台选 **Kiro**，把上面 JSON 填进对应框：

| 后台字段 | 填 |
| --- | --- |
| Access Token | `access_token` |
| Refresh Token | `refresh_token` |
| Region | `us-east-1` |
| 认证方式 | IdC（组织 SSO）/ Social（社交登录） |
| Client ID / Client Secret | `client_id` / `client_secret`（仅 IdC） |
| Profile ARN | 留空 |
| ☑ 接受 Kiro 服务条款 | **必须勾** |

保存。

**4. 挂分组**
把账号挂到对应 group，按常规设 RPM / 并发即可（和其它平台一样）。账号挂进 group 且可调度后，Kiro 即开始服务——和其它平台一样「有可调度账号就在线」，无需任何额外开关。

## 常见问题

| 现象 | 处理 |
| --- | --- |
| 调用报 `No available accounts`（429） | 账号没挂进对应 group，或账号不可调度（见第 3–4 步） |
| 创建提示要确认 ToS | 勾「接受 Kiro 服务条款」 |
| 创建提示缺 client_id/secret | IdC 必须填这两个 |
| 持续 401 / 刷新失败 | 回第 1 步重登 Kiro，重跑第 2 步，**编辑**账号粘新 token |

> Access Token 过期不用管，系统会用 refresh token 自动续。
