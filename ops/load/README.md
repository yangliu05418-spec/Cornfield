# 负载测试

这些脚本只用于隔离的预发布环境。两者都要求显式设置 `ALLOW_LOAD_TEST=true`；批次创建脚本还要求 `CONFIRM_MOCK_PROVIDER=true`，避免把 100 RPS 转换为真实 Provider 账单。`BASE_URL` 必须是 HTTPS origin，只有 localhost/loopback 可使用 HTTP。不要把 Provider key 传给 k6，也不要把 session/CSRF 值提交到仓库或粘贴进测试报告。

## 准备会话

`SESSION_COOKIE` 是完整的 Cookie header 值，例如生产环境的 `__Host-session=...`；开发环境通常是 `studio_session=...`。`CSRF` 是登录响应中的 `csrf_token`。单值可以直接传入；多个值使用 JSON 字符串数组，数组下标必须一一对应：

```text
SESSION_COOKIE=["studio_session=session-1","studio_session=session-2"]
CSRF=["csrf-1","csrf-2"]
```

使用专用负载测试账号，测试后立即撤销这些 session。不要在 shell 中 `echo` 变量，也不要启用 k6 HTTP debug；它们会暴露 Cookie 和 CSRF。建议从权限为 `0600` 的临时环境文件导出，结束后 `unset SESSION_COOKIE CSRF`。

## 100 RPS 批次创建

默认场景为 100 RPS、持续 10 秒、每次 1 draw，门槛为创建成功率 `>99%`、创建接口 p95 `<250ms`、HTTP 失败率 `<1%` 且无 dropped iteration。它必须指向 `PROVIDER_MODE=mock` 的隔离部署。

应用按用户限制 burst 4；要真实覆盖 1,000 次事务化批次创建，默认场景至少需要 250 组不同用户的 session/CSRF。脚本会在数量不足时拒绝启动，避免大量 `429` 被误解成容量问题。`MODEL_REVISION` 从 `GET /api/v1/models` 的 `revision` 取得；默认模型是 `openrouter-gpt-image-1`。

```bash
export ALLOW_LOAD_TEST=true
export CONFIRM_MOCK_PROVIDER=true
export BASE_URL=https://isolated-load.example
export MODEL_REVISION='<capability hash>'
export SESSION_COOKIE='<JSON array of dedicated test cookies>'
export CSRF='<matching JSON array>'
k6 run ops/load/generation-burst.js
```

可用 `RATE`、`DURATION_SECONDS`、`MODEL_ID`、`ASPECT_RATIO`、`RESOLUTION` 覆盖默认值。降低场景规模时，最少 session 数仍按 `ceil(RATE * DURATION_SECONDS / 4)` 校验。

## 200 个 SSE 会话

默认场景并发保持 200 条连接 30 秒，门槛为至少打开 200 条连接、连接成功率 `>99%`、建连 p95 `<1s`，且无建连错误或提前断开。API 每个用户最多保持 4 条 SSE，因此默认场景需要至少 50 个专用用户 session；脚本会在数量不足时拒绝启动。

脚本使用社区 [xk6-sse](https://github.com/phymbert/xk6-sse) 的 `k6/x/sse` 模块。使用支持动态 extension resolution 的新版 k6，并在受控环境中固定、验证 k6 与扩展版本；不能让生产主机临时下载未审计的二进制。

```bash
export ALLOW_LOAD_TEST=true
export BASE_URL=https://isolated-load.example
export SESSION_COOKIE='<JSON array of at least 50 dedicated user cookies>'
k6 run ops/load/sse-connections.js
```

可用 `SSE_CONNECTIONS` 和 `DURATION_SECONDS` 调整规模。运行时同时观察 API `/metrics` 的 `image_studio_sse_connections`、数据库连接数、CPU、内存和 Nginx 连接数；脚本结束后该 gauge 应回落到基线。
