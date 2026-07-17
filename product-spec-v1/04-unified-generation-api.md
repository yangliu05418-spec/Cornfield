# 04 · Legnext / OpenRouter 统一任务接口

## 1. 总体约束

- 浏览器只调用内部 API，不直接访问 Legnext/OpenRouter。
- 对前端而言，所有生成都是异步批次；即使 OpenRouter 的上游 HTTP 是同步返回，也由 worker 执行。
- 一次用户提交创建一个 `generation_batch`，`draw_count` 创建一个或多个内部 `generation_job`。
- 供应商返回的图片必须完成下载、校验，并原子提交到本地不可变资产树后才标记为可用。
- `Idempotency-Key` 是创建接口必填项，避免双击、刷新或网关重试导致重复扣量。

## 2. API 清单

| 方法 | 路径 | 说明 |
|---|---|---|
| `GET` | `/api/v1/models` | 获取已启用模型与能力摘要 |
| `POST` | `/api/v1/uploads` | 创建受限上传 session |
| `PUT` | `/api/v1/uploads/{id}/content` | 同源流式上传到 quarantine |
| `GET` | `/api/v1/uploads/{id}` | 查询校验状态与最终 asset ID |
| `POST` | `/api/v1/generations` | 创建生成批次 |
| `GET` | `/api/v1/generations/{batch_id}` | 获取批次、子任务和输出 |
| `GET` | `/api/v1/generations` | 游标分页查询批次 |
| `POST` | `/api/v1/generations/{batch_id}/retry` | 只重试失败的 draw |
| `POST` | `/api/v1/generations/{batch_id}/cancel` | 尽力取消批次内全部非终态 draw |
| `POST` | `/api/v1/generations/{batch_id}/jobs/{job_id}/cancel` | 取消占位卡对应的一次 draw |
| `GET` | `/api/v1/events` | 当前身份的 SSE 任务事件 |
| `GET` | `/api/v1/assets` | 游标分页查询资产 |
| `GET` | `/api/v1/assets/{asset_id}` | 获取资产详情与血缘 |
| `GET` | `/api/v1/assets/{asset_id}/content?variant=` | 鉴权后通过 X-Accel-Redirect 返回原图/缩略图 |

## 3. 获取模型

```http
GET /api/v1/models
```

```json
{
  "revision": "7c1f...64-hex-capability-hash",
  "models": [
    {
      "id": "midjourney",
      "display_name": "Midjourney",
      "provider": "legnext",
      "outputs_per_draw": 4,
      "capabilities": {
        "text_to_image": true,
        "image_to_image": true,
        "aspect_ratios": ["1:1", "3:4", "4:3", "9:16", "16:9"],
        "resolutions": ["MODEL_DEFAULT"],
        "max_reference_images": 5,
        "max_reference_bytes": 10485760,
        "draw_count": { "min": 1, "max": 4, "default": 1 }
      }
    }
  ]
}
```

`revision` 是当前 catalog 的 SHA-256 capability hash；创建批次时必须原样回传，若部署期间发生变化则返回 409 `CAPABILITY_STALE`。TanStack Query 与 API 进程内缓存承担热读，V1 当前响应不承诺 HTTP 304。

## 4. 参考图上传

```http
POST /api/v1/uploads
Content-Type: application/json
X-CSRF-Token: <session-csrf-token>
```

```json
{
  "filename": "reference.png",
  "media_type": "image/png",
  "size": 2849012
}
```

```json
{
  "id": "019...",
  "status": "created",
  "content_url": "/api/v1/uploads/019.../content"
}
```

浏览器随后向同源 `content_url` 执行 `PUT`，携带 CSRF token、声明的 `Content-Type` 和原始图片字节。Nginx 对该路由关闭 request buffering；API 用 `MaxBytesReader` 将最多 25 MiB 的 JPEG/PNG/WebP 流式写入 quarantine，拒绝 Content-Length、MIME/魔数不一致或重复 session。Worker 用 libvips 完整解码，校验最大 8192×8192/像素上限，按 SHA-256 原子提交到本地不可变资产树并生成 320/640/1280 WebP。客户端轮询 `GET /api/v1/uploads/{id}`，只有返回 `status=ready` 和 `asset_id` 后才能把该资产用于生成；session 默认 1 小时过期。创建批次时还会按所选模型的 `max_reference_bytes` 逐张校验，超限返回 422 `REFERENCE_TOO_LARGE`；因此全局上传上限不会掩盖更严格的 Provider 限制。

## 5. 创建生成批次

```http
POST /api/v1/generations
Idempotency-Key: <new-uuid-v4-for-this-request>
Content-Type: application/json
```

```json
{
  "model_id": "midjourney",
  "capability_revision": "7c1f...64-hex-capability-hash",
  "prompt": "Editorial product photo, translucent blue glass...",
  "input_asset_ids": ["01900000-0000-7000-8000-000000000001"],
  "aspect_ratio": "16:9",
  "resolution": "MODEL_DEFAULT",
  "draw_count": 2
}
```

响应必须快速返回，不等待供应商：

```http
HTTP/1.1 201 Created
```

```json
{
  "id": "01900000-0000-7000-8000-000000000010",
  "model_id": "midjourney",
  "prompt": "Editorial product photo, translucent blue glass...",
  "aspect_ratio": "16:9",
  "resolution": "MODEL_DEFAULT",
  "status": "queued",
  "draw_count": 2,
  "expected_outputs": 8,
  "completed_outputs": 0,
  "jobs": [
    { "id": "01900000-0000-7000-8000-000000000011", "draw_index": 0, "status": "queued", "expected_outputs": 4, "outputs": [] },
    { "id": "01900000-0000-7000-8000-000000000012", "draw_index": 1, "status": "queued", "expected_outputs": 4, "outputs": [] }
  ],
  "created_at": "2026-07-17T08:00:00Z"
}
```

同一身份 + 同一 `Idempotency-Key` + 相同 body 返回同一批次；key 相同但 body 不同返回 409 `IDEMPOTENCY_CONFLICT`。

## 6. 批次状态

### 6.1 规范化状态机

```text
queued → dispatched → submitting → provider_pending → ingesting → succeeded
                         │                  │              └──────→ failed
                         ├────→ submission_uncertain
                         └────→ ingesting  (OpenRouter 同步结果)
可取消非终态（不含 submission_uncertain）→ cancelling → cancelled
                                                └─ 可选上游占用租约（不改变用户可见终态）
```

批次状态由子任务归并：

- 全部成功：`succeeded`
- 成功与失败并存：`partial`
- 无成功且全部终态失败：`failed`
- 尚有非终态子任务：`running`
- 全部在提交前取消：`cancelled`

### 6.2 占位卡与 Job 的映射

- API 创建批次时立即返回 `expected_outputs`，前端据此创建占位卡。
- 每个占位卡携带 `job_id` 和 `output_index`。图片完成后，`job.succeeded` payload 的 outputs 使用同一对字段完成原位替换。
- 一个 job 产生多张图片时，多张占位卡共享 `job_id`；取消其中任意一张等同取消整个 draw，不提供供应商无法兑现的“单输出取消”。
- 占位卡的宽高比例来自标准化参数；实际图片尺寸不一致时在框内使用 cover/contain 策略，不引发墙面整体跳动。

### 6.3 供应商状态映射

| 内部状态 | Legnext | OpenRouter worker |
|---|---|---|
| `queued` | 尚未提交 | 尚未发起上游请求 |
| `submitting` | `POST /diffusion` 中 | `POST /images` 中 |
| `provider_pending` | `pending` / `staged` / `processing` | 不适用；OpenRouter 同步请求仍在 Worker 内 |
| `ingesting` | Provider 已完成，正在复制/校验资产 | 已取得完整响应，正在解码/入库 |
| `succeeded` | `completed` 且资产入库完成 | 完整响应且资产入库完成 |
| `failed` | `failed`、deadline 或不可重试错误 | 非 2xx、deadline、base64/图片解码失败 |
| `submission_uncertain` | POST 可能已接受但未拿到远端 ID | POST 可能已计费但同步响应不可恢复 |

`succeeded` 的定义不是“供应商说完成”，而是“所有成功输出已安全写入自有存储并产生数据库资产”。

`submission_uncertain` 冻结用户可操作状态，但如果请求可能已到达 Provider，会把“原生成 deadline + 有界观察宽限”记录为保守上游占用并继续计入并发；宽限通常等于模型生成 timeout，最多 1 小时。只有管理员按运维手册对账、观察到远端终态、确认结果不可恢复或硬 deadline 到期后才解除。它不是可由普通取消/重试绕过的空闲任务。

## 7. 取消单次 Draw

```http
POST /api/v1/generations/019.../jobs/019.../cancel
X-CSRF-Token: <session-csrf-token>
```

```json
{
  "status": "cancelling",
  "cancel_mode": "discard_result_only",
  "cost_may_have_been_incurred": true
}
```

`cancel_mode` 取值：

- `local`：任务仍为 queued/dispatched 且没有 Provider job ID，确定不会产生本次上游任务。
- `requested_upstream`：供应商支持取消且已接受请求；业务任务立即取消，不保留上游占用租约。
- `discard_result_only`：供应商不能可靠取消；业务任务尽快转为用户可见的 `cancelled`，迟到输出不入用户资产墙，上游仍可能产生消耗。

取消控制接口可能先返回 `cancelling`；Worker 完成本地取消决议后立即发出 `job.cancelled`，不等待远端生成结束。若 Provider 没有接受真实取消，系统把“原 `generation_deadline` + 有界观察宽限”持久化为 `upstream_active_until`：在远端终态或该硬截止前，此任务继续计入用户、Provider 和模型并发；宽限通常等于模型生成 timeout，最多 1 小时。有可轮询的 Provider job ID 时，River 通常每 3 秒观察一次，callback 只用于提前唤醒；观察到远端成功、失败或取消终态即清除租约并停止执行，任何取消后的迟到结果均丢弃。没有 Provider job ID 的模糊提交无法安全轮询，只能在硬截止到期后释放。Provider 明确接受取消或任务在提交前本地取消时不保留租约。

正常执行到达 `generation_deadline` 时，Worker 必须先做一次最长 45 秒的认证 final poll，避免丢失在截止点前刚完成的已付费结果：已完成则进入入库，远端已失败则保存其失败；若仍非终态，才尝试取消并写入 `failed/PROVIDER_TIMEOUT`。远端取消未被接受时，`failed` 任务同样保留上述观察租约，观察到远端终态会提前释放，但不会把已经发布给用户的失败终态改回成功。

接口幂等；已终态成功/失败返回 409 `JOB_ALREADY_FINISHED`，重复取消返回当前取消状态。只有 batch 所有者或管理员可取消。

## 8. 查询批次

```http
GET /api/v1/generations/019...
```

```json
{
  "id": "019...",
  "model_id": "midjourney",
  "status": "partial",
  "prompt": "Editorial product photo...",
  "aspect_ratio": "16:9",
  "resolution": "MODEL_DEFAULT",
  "draw_count": 2,
  "expected_outputs": 8,
  "completed_outputs": 4,
  "jobs": [
    {
      "id": "019...011",
      "draw_index": 0,
      "status": "succeeded",
      "expected_outputs": 4,
      "outputs": [
        {
          "asset_id": "019...101",
          "output_index": 0,
          "width": 1792,
          "height": 1024,
          "mime_type": "image/png",
          "url": "/api/v1/assets/019...101/content",
          "thumb_320_url": "/api/v1/assets/019...101/content?variant=320",
          "thumb_640_url": "/api/v1/assets/019...101/content?variant=640",
          "thumb_1280_url": "/api/v1/assets/019...101/content?variant=1280"
        }
      ]
    },
    {
      "id": "019...012",
      "draw_index": 1,
      "status": "failed",
      "expected_outputs": 4,
      "error_code": "PROVIDER_RATE_LIMITED",
      "error_message": "供应商繁忙，本次抽卡可重试",
      "outputs": []
    }
  ],
  "created_at": "2026-07-17T08:00:00Z"
}
```

供应商原始错误和原始请求仅写入受限日志/JSON 字段，默认不返回浏览器。

## 9. SSE 事件

```http
GET /api/v1/events?after=4711
Accept: text/event-stream
```

```text
id: 4712
event: job
data: {"id":4712,"type":"job.updated","batch_id":"019...","job_id":"019...011","payload":{"status":"provider_pending"},"created_at":"2026-07-17T08:00:10Z"}

id: 4713
event: job
data: {"id":4713,"type":"job.succeeded","batch_id":"019...","job_id":"019...011","payload":{"status":"succeeded","outputs":[{"asset_id":"019...101","output_index":0}]},"created_at":"2026-07-17T08:01:04Z"}
```

事件类型：

- `batch.created`、`batch.updated`、`batch.completed`、`batch.failed`
- `job.queued`、`job.submitting`、`job.updated`
- `job.retry_scheduled`、`job.submission_uncertain`
- `job.cancelling`、`job.cancelled`、`job.failed`、`job.succeeded`

客户端断线后通过 `Last-Event-ID`（或数字 `after` query）重连；游标已过 90 天保留期时服务端发送 `event: reset`，客户端重新查询批次/资产快照。新连接最多回放最近 500 条，心跳 15 秒；SSE 只发小型元数据，不传 prompt、base64 图片或二进制。

## 10. Provider Adapter 接口

```go
type ProviderAdapter interface {
    Submit(context.Context, CanonicalRequest) (Submission, error)
    Poll(context.Context, Submission) (Result, error)
    Cancel(context.Context, Submission) (CancelResult, error)
    Probe(context.Context) Health
}

type Result struct {
    Status    string
    Images    []Image
    Usage     map[string]any
    ErrorCode string
    ErrorText string
    Telemetry Telemetry // bounded request ID + HTTP status only
}
```

适配器不直接写业务表。worker 负责状态机、幂等、重试和事务；适配器只负责协议转换与供应商通信。

## 11. Legnext 适配

### 11.1 提交

内部请求：

```json
{
  "prompt": "A glass perfume bottle",
  "references": ["https://signed-media.internal/ref.png"],
  "aspect_ratio": "16:9",
  "model_version": "8.1"
}
```

映射到：

```http
POST https://api.legnext.ai/api/v1/diffusion
x-api-key: ${LEGNEXT_API_KEY}
```

```json
{
  "text": "https://signed-media.internal/ref.png A glass perfume bottle --ar 16:9 --v 8.1",
  "callback": "https://studio.internal.example/api/v1/provider-callbacks/legnext/{generation_job_id}/{hmac_signature}"
}
```

保存响应 `job_id`，状态进入 `provider_pending`。供应商访问参考图使用短期签名 HTTPS URL，有效期至少覆盖任务 timeout + 10 分钟。

### 11.2 完成检测

- callback 负责低延迟唤醒 worker。
- `GET https://api.legnext.ai/api/v1/job/{job_id}` 是状态事实来源。
- 当前 River job 对非终态结果 `Snooze` 3 秒再轮询，每次 Poll HTTP 最多 45 秒且不越过 generation deadline；429/5xx 走统一 Retry-After/指数退避与 full jitter，而不是高频自旋。
- callback 即使到达，也重新 GET job 校验，不直接信任 callback body。

官方文档当前说明 callback 字段，但未公开可依赖的 Provider 侧签名规则。因此系统为每个本地 generation job 生成 HMAC 路径签名，同时配置 IP 限速、64 KiB body 上限、事件去重/数量/时间窗口；最终状态仍必须通过服务端 API Key 回查确认。

完成后读取 `output.image_urls`；逐个通过 SSRF 防护和模型快照中的精确 hostname allowlist 下载、校验内容，并写入本地 staged output/不可变资产树。远程 URL 可能是临时资源，不能作为长期资产地址。CDN host 漂移必须拒绝下载并由人工 canary、域名归属核查、静态配置代码评审和重新发布处理；禁止自动学习域名、通配符放行、私网访问或跟随重定向。

## 12. OpenRouter 适配

```http
POST https://openrouter.ai/api/v1/images
Authorization: Bearer ${OPENROUTER_API_KEY}
Content-Type: application/json
```

```json
{
  "model": "bytedance-seed/seedream-4.5",
  "prompt": "A glass perfume bottle",
  "n": 1,
  "resolution": "2K",
  "aspect_ratio": "16:9",
  "input_references": [
    { "type": "image_url", "image_url": { "url": "https://signed-media.internal/ref.png" } }
  ],
  "provider": {
    "only": ["bytedance"],
    "allow_fallbacks": false
  }
}
```

- V1 Worker 使用 token-wise JSON decoder 并将响应体限制为 160 MiB、单张解码结果限制为 50 MiB；任何 partial/base64 都不传浏览器、不写数据库/日志。
- 对支持原生 SSE 的模型，后续可在 worker 内消费 partial image 并转成临时预览；能力协议已预留 `can_stream_preview`。
- `data[].b64_json` 在 Worker 内有界解码后立即写入 staged output，再校验并原子提交到本地不可变资产树；V1 仍会为单张图片分配一个受 50 MiB 上限保护的解码 buffer，不把它误称为零拷贝。
- `media_type` 负责输出格式校验；缺失时以魔数探测，禁止只信扩展名。
- 模型支持 `n` 时可由适配器合并同批 draw 以降低连接开销，但内部仍保留逐 draw 可追踪语义。V1 可先一 draw 一请求，以保证失败重试简单。

## 13. 重试、限流与幂等

| 场景 | 策略 |
|---|---|
| 网络超时且不确定是否已创建 Legnext Job | 先按内部 job 查询是否已有 `provider_job_id`；禁止盲目重提 |
| 429 | 指数退避 + jitter，尊重 `Retry-After`，占用供应商级限流令牌 |
| OpenRouter Submit 502 | 官方定义为未完成/已取消且不计费，按能力协议有限重试并尊重 `Retry-After` |
| 其他 Submit 5xx/网络中断 | 无法确认是否已接受时进入 `submission_uncertain`，不自动重提 |
| Poll/下载 5xx | 安全重试，按 Retry-After/指数退避与 full jitter |
| 400/401/403/422 | 不重试，映射为可理解错误；供应商降级时通知管理员 |
| 资产下载失败 | 不重新生成；只重试远程文件落库 |
| 单 draw 失败 | 其他 draw 继续；批次进入 partial 或 failed |

队列至少按 provider + model 分区控制并发。API 接收层不以供应商并发为由长时间阻塞，而是返回 queued。

## 14. 规范化错误码

| code | HTTP/任务语义 | 用户文案 |
|---|---|---|
| `CAPABILITY_INVALID` | 422 | 参数不符合当前模型能力 |
| `CAPABILITY_STALE` | 409 | 模型能力已更新，请刷新后重试 |
| `IDEMPOTENCY_CONFLICT` | 409 | 请求标识已用于另一组参数 |
| `REFERENCE_NOT_FOUND` / `REFERENCE_UNAVAILABLE` | 422/409 | 参考图不存在、已过期或仍不可用 |
| `PROVIDER_UNAUTHORIZED` | 失败 | 供应商连接异常，已通知管理员 |
| `PROVIDER_QUOTA_EXHAUSTED` | 失败 | 供应商额度不足，已通知管理员 |
| `PROVIDER_RATE_LIMITED` | 排队/失败 | 供应商繁忙，可稍后重试 |
| `PROVIDER_REJECTED_CONTENT` | 失败 | 内容被模型服务拒绝 |
| `PROVIDER_TIMEOUT` | 失败 | 生成超时，可重试本次 |
| `OUTPUT_INGEST_FAILED` | 失败 | 图片保存失败，系统正在重试 |
| `JOB_NOT_CANCELLABLE` | 409 | 任务已经终止或需要管理员对账，无法取消 |
| `INTERNAL_ERROR` | 失败 | 系统异常，请稍后重试 |

## 15. 安全要求

- API Key 只通过 UID `65532` 可读、mode `0600` 的 file-backed secret 挂载到 Worker；不写数据库/环境变量/日志，不挂载到 API，也不返回客户端。
- 日志中的 prompt 与图片 URL 做访问控制；错误日志删除签名 query。
- 下载供应商输出需防 SSRF：仅 HTTPS、DNS/IP 校验、禁止私网地址、限制重定向/大小/超时；Legnext 已知 CDN 可配置 allowlist。
- 上传文件校验魔数、像素上限、解码炸弹和恶意 SVG；V1 参考图不接受 SVG。
- 内部媒体 URL 需要鉴权或短期签名，不能默认公开。

## 16. 官方依据

- [Legnext Text to Image](https://docs.legnext.ai/api-reference/image-generation/diffusion)
- [Legnext Get Task](https://docs.legnext.ai/api-reference/task-management/get-task)
- [OpenRouter Dedicated Image API](https://openrouter.ai/docs/guides/overview/multimodal/image-generation)
