# 运维手册

## 上线门槛

- GitHub CI 全绿：Go test/vet/race/build、模型配置校验、前端 check/typecheck/lint/unit/E2E/build、镜像构建、Compose 配置与 fresh-database mock smoke 均通过。
- `PROVIDER_MODE=live`；四个互不相同的数据库密码、两个 Provider key 与两个相互独立的内部签名 secret 均存在。file-backed secret 的宿主源文件由 `65532:65532` 持有且权限为 `0600`；内部签名 secret 均不少于 32 字节。Grafana 密码单独由 `472:472` 持有，Alertmanager webhook URL 单独由 `65534:65534` 持有，权限均为 `0600`。
- `studio_bootstrap` 是唯一数据库 bootstrap superuser；`studio_owner`、`studio_api`、`studio_worker` 均为 `NOSUPERUSER NOCREATEDB NOCREATEROLE NOREPLICATION NOBYPASSRLS`。API 不能读取 River 表，Worker 不能读取 session token/hash。数据库密码只通过 `DATABASE_PASSWORD_FILE` 读取，不出现在 `.env`、连接 URL、命令行或 `.pgpass`。
- `APP_ENV=production`、`SESSION_COOKIE_SECURE=true`；`APP_PUBLIC_URL` 是使用标准 443 端口、不含账号、路径、query 或 fragment 的 bare HTTPS origin，其 host 与宿主 Nginx 的 `server_name` 完全一致。
- `.env` 中的 `DATA_ROOT=/srv/internal-image-studio/data` 与 Nginx alias、备份环境一致；`data`/`assets` 为 `65532:65532 0750`，`uploads`/`tmp`/`quarantine` 为 `65532:65532 0700`，UID `65532` 已实测可写。Compose 中 API 的 `/data` 父挂载只读，仅 `/data/uploads` 通过嵌套挂载可写；Worker 才能写完整数据树。宿主存在 GID `65532` 的 `cornfield-runtime` 组，实际 Nginx worker 用户与所有当前 worker 进程都已继承该组，因此能只读 traverse/读取资产；数据目录、PostgreSQL volume 和 Restic 仓库均不在临时磁盘。
- `modelctl validate`、`modelctl apply` 使用同一份 `config/models.yaml`，API readiness 返回的 `model_revision` 与本次发布预期一致。
- GitHub release workflow 已为目标 commit 生成并扫描 API、Worker、Tools、Web 四个镜像；Tools 镜像使用与服务端同主/小版本的、digest 固定的 `postgres:18.4-bookworm` 工具链，不能退回 Debian 默认 PostgreSQL 15 client。下载的 `cornfield-image-digests-<commit>` 通过 `sha256sum -c SHA256SUMS`，`RELEASE_COMMIT` 与待部署 commit 一致，`.env` 中四个 `*_IMAGE` 均来自该 artifact 且按 `@sha256:` 固定，`RELEASE_REQUIRE_DIGESTS=true`。
- 宿主 Nginx、站点配置和 TLS 证书/私钥已先安装；`NGINX_SITE_CONFIG` 指向的已启用文件与仓库审查版本逐字一致并出现在 `nginx -T`，`NGINX_WORKER_USER` 与主配置的 `user` 一致。以 root 执行 `STUDIO_ROOT=/opt/internal-image-studio ops/preflight.sh` 并通过。随后 `docker compose ps` 中 Web、API、Worker、PostgreSQL 均 healthy，`docker compose ps -a db-bootstrap migrate model-apply` 显示三个一次性 job 成功退出。
- Nginx 的 `/_protected_assets/` 保持 `internal`；Compose 的 `frontend` 是宿主可达的普通 bridge，但 Web/API 端口与该网络的默认 host binding 都固定为 `127.0.0.1`。PostgreSQL 只连接 internal `backend` 且不映射宿主端口；Worker 同时连接 internal `backend` 与 `egress`，API 不连接命名的 `egress` 网络，也不持有 Provider key。
- 从宿主 Nginx 访问落地页和创作页，确认 CSP 控制台无阻断、SPA 可交互、墙面 inline geometry 生效。Web 构建必须把 TanStack bootstrap 外置为同源脚本；`preflight.sh` 会拒绝 `script-src 'unsafe-inline'`。
- Provider callback 与签名图片 URL 不出现在 Nginx/API access log；真实 Provider canary、最近一次备份和恢复演练均有记录。Prometheus 规则已接入并实际触发过外部 receiver；没有可送达且已演练的 receiver 时不得宣布生产上线。

## 健康检查

健康信号分层使用，不能用单个 `up` 代替业务健康：

- `GET http://127.0.0.1:8081/health/live` 只证明 API 进程可以响应。
- `GET http://127.0.0.1:8081/health/ready` 在 1 秒窗口内检查 PostgreSQL，并确认当前 YAML capability hash 已写入 `models`，且每个当前模型在该 revision 下都有不可变能力快照。快照内容冲突由 `modelctl apply` 拒绝。失败返回 `503`，Compose 的 API healthcheck 调用此端点。
- Worker 每 10 秒用容器 hostname 向 `service_heartbeats` 写心跳。`/worker healthcheck` 连接 PostgreSQL并要求本实例心跳不超过 45 秒；Compose 在 45 秒 start period 后使用该命令。
- Worker 心跳在 River 启动且调度、维护、上传验证和 Provider probe 循环已创建后才开始；它能发现进程/数据库/主循环启动失败，但不能单独证明队列正在推进。必须同时观察队列年龄和任务状态。
- Web 的 `/health` 只验证静态 Nginx 容器；PostgreSQL 使用 `pg_isready`。

发布后执行：

```bash
docker compose ps
curl -fsS http://127.0.0.1:8081/health/live
curl -fsS http://127.0.0.1:8081/health/ready
docker compose exec -T worker /worker healthcheck
curl -fsS http://127.0.0.1:8080/health
```

API readiness 不检查 Worker、Provider、磁盘可写或公网 callback。对应信号分别来自 Worker healthcheck、Provider probe/真实 canary、磁盘指标和外网回调验证。

## Fresh-database Compose smoke

CI 的 image job 在全新 Compose project 和临时 `DATA_ROOT` 上执行 `CI=true bash ops/ci-smoke.sh`。该 smoke 覆盖：

- 空 PostgreSQL 上的应用/River migration 和模型能力快照应用；
- bootstrap/owner/API/Worker 角色分离、数据库/Schema owner、API 与 Worker 的最小表/列权限；
- 管理员创建、登录、Cookie/CSRF，以及粘性 Provider 暂停的受审计恢复；
- 流式图像上传、quarantine、libvips 解码、内容寻址入库；
- mock 模式图生图、`provider_attempts.usage.reference_count=1`、Idempotency-Key 重放、River/Worker 执行和输出资产；
- X-Accel-Redirect 响应、SSE 事件，以及四个长期服务保持 running。

脚本只在 `/tmp/cornfield-ci-{data,secrets,http}.*` 创建一次性数据与 secret，通过 `*_SECRET_SOURCE` 指向这些文件，不读取或改写仓库的 `secrets/`。cleanup 会先关闭独立 Compose project，再仅删除经过固定 `/tmp` 前缀校验的目录；脚本仍只允许在一次性 CI runner/disposable checkout 运行。不要在生产 checkout 中手工设置 `CI=true` 执行。它验证内部完整链路，但不访问 Legnext/OpenRouter，也不验证宿主 Nginx、TLS、Provider callback 或真实计费。

## 真实 Provider 验证边界

静态与非计费验证分三层：

1. `modelctl validate` 校验本地 schema 和能力约束。
2. `modelctl verify-remote` 使用 OpenRouter capability API 做只读 drift 检查；Legnext 没有可机读的 capability endpoint，因此会明确显示 skipped。
3. live Worker 每 30 秒探测一次：OpenRouter 校验 key，Legnext 查询余额；结果写入 `providers` 并显示在管理员 Provider 页面。

在有 Go 工具链的受控发布机上执行 OpenRouter drift 检查，key 只通过文件读取：

```bash
cd /opt/internal-image-studio/backend
MODEL_CONFIG_PATH=../config/models.yaml \
OPENROUTER_API_KEY_FILE=../secrets/openrouter_api_key \
go run ./cmd/modelctl verify-remote
```

上述检查均不会创建图片，也不能证明生成协议、图生图参考 URL、callback 公网可达、结果下载、取消语义或最终费用正确。真实 Provider key 只挂载到 Worker，API 只持有 callback/短期资产 URL 的内部签名 secret。付费 canary 前必须先确认宿主 Nginx/TLS 已上线，并从公网验证 Provider 能访问短期签名的 `GET/HEAD /api/v1/provider-assets/...`；Legnext 的 `POST /api/v1/provider-callbacks/...` 还必须能通过公网到达 API。不要把完整签名 URL 记录到终端历史或工单。

首次上线和 Provider 合约变化后必须在公开 HTTPS 部署上用专用、设有低额度上限的测试用户执行小规模真实 canary；这一步必须人工触发，不能由 CI、健康探针或负载脚本自动执行：

- OpenRouter：各 1 次文生图和图生图，1 draw、1:1、1K。
- Legnext：各 1 次文生图和图生图，1 draw；预期一个 draw 返回模型配置声明的多张输出。
- 对每次任务记录本地 batch/job ID、Provider request/job ID、开始/结束时间和实际用量；确认刷新与 SSE 重连可恢复、最终资产已复制到本地、下载可用、上游临时 URL 失效后历史资产仍可读。
- 观察 callback 命中和主动 poll 对账；不要把 key、签名 URL、prompt-bearing callback body 或 base64 输出复制进工单/日志。

已验证基线（2026-07-17）：Legnext 使用 GitHub 托管的 Go logo 公共参考图、image prompt、`--iw 2` 与 Midjourney V7 draft 模式完成一次真实图生图 canary，约 35 秒返回 4 张图片；视觉结果明确保留 Go 标识，确认参考图提交、异步轮询与多输出下载链路可用。该次输出的 CDN host 为 `img.playjoy3d.com`，已作为精确 hostname 加入 `config/models.yaml` 的 `allowed_output_hosts`；下载仍由 safe HTTP client 拒绝私网地址和重定向，不能因 canary 成功绕过这些检查。

同日 OpenRouter Dedicated Image API 使用 `openai/gpt-image-1`、1 张参考图和 `input_references` 完成一次真实图生图 canary，返回 HTTP `200`，base64 解码后得到一张 1,142,980 字节的有效图片。该结果确认当前请求与响应协议，但这两项 Provider 直连基线都没有经过生产域名上的 Cornfield 短签资产入口；只有部署后从 UI 上传参考图并完成生成、SSE、入库和下载，才算端到端 canary。

Provider CDN 域名漂移必须 fail closed。出现 `OUTPUT_HOST_REJECTED` 时，不得自动学习新域名、添加通配符、跟随重定向或临时关闭 SSRF 防护；只记录脱敏后的 hostname 与本地 job ID，不记录完整签名 URL。运维人员需核对 Provider 通知/文档、DNS/TLS 与域名归属，在低额度专用用户上重跑最小真实 canary；确认通过后，以精确 hostname 修改静态模型配置，经代码评审、`modelctl validate`、`modelctl apply` 和正常发布生效，再复跑 canary。旧 host 只有在确认 Provider 不再使用且队列中没有依赖旧 capability revision 的任务后才能移除。

Legnext 官方同时用 HTTP `403` 表示敏感内容和权限不足。Cornfield 只在结构化错误明确指出敏感内容时将其视为单请求内容策略拒绝；权限或无法明确分类的 `403` 会 fail closed 并暂停 Provider。暂停是粘性状态：余额/key 健康探针仍会更新时间，但不会覆盖暂停原因或自动恢复发布。收到 `ImageStudioProviderPaused` 后，先在 Legnext 控制台核对 diffusion 权限与 key 状态，再由管理员在 `/app/admin/providers` 明确确认风险并恢复；该操作受登录、管理员权限、CSRF 和事务审计保护，恢复前禁止把未知 `403` 批量重试为用户内容错误。

自动化运维也可以调用 `POST /api/v1/admin/providers/<provider-id>/resume`，必须携带管理员 session 与 `X-CSRF-Token`。接口只接受已启用且当前为 `paused` 的 Provider，把它转为 `degraded` 并清除持久化 breaker/暂停错误；后续健康探针再把它提升为 `healthy`。不要直接 `UPDATE providers` 绕过 `provider.resume` 审计记录；恢复会重新允许付费提交，只能在权限、额度或 API Key 根因已确认解决后执行。

负载测试绝不能指向 live Provider；参见 [负载测试说明](../ops/load/README.md)。

## 取消/超时后的上游占用租约

取消/超时分成“用户可见终态”和“上游是否仍占用资源”两个维度。任务已经提交到不支持可靠取消的 Provider 后，Worker 会尽快把用户取消的业务状态写为 `cancelled`，前端不再等待或展示该结果。正常任务到达 `generation_deadline` 时，Worker 会先执行一次最长 45 秒的认证 final poll：已完成则继续入库，远端已失败则记录其失败；只有仍非终态且无法可靠取消时才写为 `failed/PROVIDER_TIMEOUT`。这两类终态都可在 `generation_jobs.upstream_active_until` 保留保守租约，硬截止为原 `generation_deadline` 加有界观察宽限；宽限通常等于模型 `generation_timeout`，最多 1 小时。该租约存在期间：

- 任务仍计入对应用户、Provider 和模型的在途并发，避免取消按钮绕过公平调度和上游并发上限；管理员 Provider 页的 `active_jobs` 也包含这类租约。
- 有远端 job ID 时，River 每 3 秒唤醒一次执行并只做状态观察；收到 callback 也只负责提前唤醒。远端到达成功、失败或取消终态后立即清除租约，不必等到硬截止。已取消任务的任何迟到图片都不会写入用户资产；超时失败任务的观察也不会把业务终态改回成功。
- 没有可轮询远端 ID 的模糊提交只能保留到硬 deadline；Worker 使用较低频唤醒检查持久化 deadline，不会重新 Submit。
- Provider 明确接受真实取消，或任务尚未提交而本地取消时，不创建上游租约。

`submission_uncertain` 也复用 `upstream_active_until` 表达“远端可能已经接受”的保守占用；管理员确认并绑定 Provider job ID 后，租约会短暂随等待重派的 queued job 保留。不要手工清空该字段来释放并发：这可能让仍在计费的远端任务与新任务同时运行。若取消或超时失败任务长期占用，应检查 `provider_attempts.operation IN ('deadline_poll','cancelled_poll')`、Provider callback/健康状态、原 generation deadline 与观察宽限；只有代码状态机或经评审的数据修复才能变更租约。Worker/数据库重启不会丢失租约，恢复后会继续观察；备份窗口停止 Worker 时只是暂停观察，硬截止仍在数据库中推进。

## 指标与告警

`/metrics` 只暴露在 internal backend 网络，由 Prometheus 拉取，不经宿主 Nginx 公网转发。API 当前导出：

- HTTP 请求数与 duration histogram（50ms–5s buckets）；
- 活跃 SSE 连接数；
- API readiness gauge `image_studio_api_ready`；它与 `/health/ready` 复用同一组 PostgreSQL、资产存储、模型 revision 和能力快照检查，即使 `/metrics` 自身仍返回 `200`，readiness 失败也会明确导出 `0`；
- 活跃/保留中的上传 session 数 `image_studio_upload_sessions` 与预留字节数 `image_studio_upload_reserved_bytes`；
- 资产盘可用百分比；
- generation job 按状态数量、最老 queued job 等待秒数；
- Provider state/enabled/breaker、最近 5 分钟 attempt outcome；
- Worker 等服务的数据库心跳年龄；
- API pgx acquired/idle 连接数。

`ops/prometheus-rules.yml` 已配置：target down、API readiness 失败或指标缺失、宿主文件系统 `<20%`/`<10%`、上传预留容量接近全局上限（预留字节大于 768 MiB 或 session 大于 24）、Worker 心跳缺失/超过 45 秒、最老队列等待超过 120 秒持续 5 分钟、`submission_uncertain`、已启用 Provider 的 paused/breaker/5 分钟内重复失败、关键 Provider 指标族缺失，以及备份/恢复演练最近一次失败。计划停用的 Provider（`enabled=false`）不会触发 paused、breaker 或历史失败告警；停用不会停止该 Provider 指标本身的导出。距上次成功备份超过 26 小时、距上次成功恢复演练超过 35 天或对应指标从未出现，也会触发 critical 告警。

应用自身的磁盘保护与告警阈值不同：

- `<20%`：告警并安排扩容/清理。
- `<15%`：API 拒绝新生成和上传；正在运行任务仍可入库。
- `<10%`：调度器停止新的 Provider 提交，只允许清理和管理操作。

Grafana 密码只从 `secrets/grafana_admin_password` 读取，不存在默认 `admin` 密码。默认核心/observability profile 不启动 Alertmanager；Prometheus 会评估规则，但在没有 active Alertmanager 时不会主动发到电话/IM。生产需要先把 generic webhook 指向现有告警平台：将单行 HTTPS URL 写入 `secrets/alertmanager_webhook_url`（或 `ALERTMANAGER_WEBHOOK_URL_SECRET_SOURCE` 指定的绝对路径），设置 owner `65534:65534`、mode `0600`，再启用并验证。Alertmanager 发送标准 generic webhook v4 JSON 且禁止跟随重定向；它不保证兼容只接受厂商自定义 payload 的 Slack/飞书机器人 URL，此类目标必须接入受控的中间 receiver 做鉴权与格式转换。

```bash
COMPOSE_PROFILES=observability,alerting STUDIO_ROOT=/opt/internal-image-studio ops/preflight.sh
docker compose --profile observability --profile alerting up -d
docker compose ps alertmanager
curl -fsS http://127.0.0.1:9093/-/ready
curl -fsS http://127.0.0.1:9090/api/v1/alertmanagers
```

最后一个响应必须把 `alertmanager:9093` 列在 `activeAlertmanagers`，并且要用可控测试告警确认外部 receiver 实际收到通知；仅看到容器 healthy 不算完成。SSE 的连接 gauge 已有，但数据库 commit 到客户端送达的端到端延迟尚无服务端 histogram，发布验收需结合 canary/负载测试测量。

## 负载验收

在与生产规格一致、`PROVIDER_MODE=mock` 的隔离环境执行：

- `ops/load/generation-burst.js`：默认 100 RPS 持续 10 秒，校验创建成功率、p95 `<250ms` 和 dropped iteration。因为每用户 burst 为 4，默认场景需要 250 组专用 session/CSRF 才能测到真实创建路径。
- `ops/load/sse-connections.js`：使用至少 50 个专用用户 session（每用户上限 4）保持 200 个并发 SSE 会话 30 秒，校验打开数量、成功率、建连 p95 `<1s`，且无提前断开。

两者都必须显式设置 `ALLOW_LOAD_TEST=true`；批次脚本还要求 `CONFIRM_MOCK_PROVIDER=true`。测试期间观察 CPU、内存、DB connections、SSE gauge、队列年龄与磁盘增长，结束后确认会话 gauge 回落、队列清空并撤销测试 session。

## 备份

安装 Restic 后设置 root-only 环境文件 `/etc/internal-image-studio/backup.env`：

```text
RESTIC_REPOSITORY=s3:https://backup.example/bucket/studio
RESTIC_PASSWORD_FILE=/etc/internal-image-studio/restic-password
AWS_ACCESS_KEY_ID=...
AWS_SECRET_ACCESS_KEY=...
STUDIO_ROOT=/opt/internal-image-studio
DATA_ROOT=/srv/internal-image-studio/data
BACKUP_STAGE=/var/backups/internal-image-studio
RESTORE_CHECK_ROOT=/mnt/cornfield-restore-scratch
RESTORE_CHECK_SAMPLE_SIZE=20
NODE_EXPORTER_TEXTFILE_DIR=/var/lib/node_exporter/textfile_collector
```

将该环境文件设为 `root:root`、`0600`。预先将 `BACKUP_STAGE` 及其 `database` 子目录都创建为 `root:root 0700`，并创建 `root:root 0755` 的 node-exporter textfile 目录；`RESTORE_CHECK_ROOT` 必须是独立恢复盘上由 root 持有且组/其他用户不可写的现有目录。`STUDIO_ROOT`、`DATA_ROOT`、`BACKUP_STAGE`、`BACKUP_STAGE/database` 和 `RESTORE_CHECK_ROOT` 都必须是绝对、已存在且已经规范化的真实路径，任一父组件或叶子都不能是 symlink；脚本在任何 Docker 操作、临时目录创建或删除前执行 `readlink -e` 等值校验。`STUDIO_ROOT` 的每个父目录、维护脚本、`config/`、`compose.yaml` 和 `.env` 必须由 root 持有且组/其他用户不可写，禁止从普通用户可写 checkout 运行 root timer。部署 `.env` 与 backup env 的 `DATA_ROOT`、`NODE_EXPORTER_TEXTFILE_DIR` 必须逐字一致。

安装 `ops/systemd/` 中的 backup、restore-check、timer 与 `internal-image-studio-maintenance-recovery.service`，执行 `systemctl daemon-reload`，启用 boot recovery 和两个 timer。backup/restore 分别使用有限的 2 小时/8 小时启动上限，停止阶段保留 8 分钟完成主进程 trap 与第二次 `ExecStopPost` 恢复，内部每个锁等待和 Docker CLI 仍有更短的独立上限，不会永久挂起。root-only `StateDirectory` 持久保存 Worker marker；marker 记录原 Worker container ID，恢复前核对 Compose project/service label，并只用 `compose start` 启动既有容器，禁止 build 或 recreate。普通失败由 EXIT trap 恢复，`SIGKILL` 由 `ExecStopPost` 恢复，主机重启由 boot recovery 恢复；恢复动作也必须先取得同一 maintenance lock。只有健康启动成功后 marker 才会删除，原本已停止的 Worker 不会被误启动。

备份与恢复共用 `BACKUP_STAGE/database/.maintenance.lock` 的非阻塞 `flock`，避免 daily backup、月度 restore、boot/ExecStopPost recovery 或人工补跑相互交错；未取得锁的进程没有 Worker recovery ownership，不能消费其他备份的 marker。持锁后，备份会先清除上次不可捕获 `SIGKILL` 留下的受控 `studio-*.dump{,.part}` staging，再通过本地 PostgreSQL socket 生成保留 owner/ACL 的 custom-format dump。Worker 停止期间 API 仍可执行短数据库事务并接收 quarantine 上传，但不会提升、覆盖或删除正式资产，因此 dump 与不可变资产树形成一致切面。脚本把 dump、实际 `DATA_ROOT`、模型/部署配置、`.env` 和 secret 目录送入加密 Restic 仓库；本地 dump 只是 staging，无论成功、pg_dump/Restic 失败还是远端 prune 失败，EXIT 都会精确删除本轮 `.part`/`.dump`，不会在根盘无界累积。

每轮 Restic backup 使用 128-bit run tag，并先标记为 `cornfield-pending`。只有命令退出 0、该 run 恰好对应一个 snapshot、snapshot paths 与预期目标完全一致且 Worker 仍静止时，脚本才把它原子提升为 `cornfield-verified`；Restic exit 3 产生的部分快照、事后一致性失败或 tag 失败永远不会进入可恢复集合。提升结果再次按 run tag 校验，失败时删除该 run；恢复演练先查询同时具有 `cornfield,cornfield-verified` 的最新精确 snapshot ID，禁止使用 generic `latest`。保留策略也只处理 verified 集合。快照验证完成后立即恢复 Worker，再执行 verified-only 的 `forget --keep-daily 7 --keep-weekly 4 --prune`。

首次生产发布前，在 mock Provider、空队列和一次性备份仓库上做一次故障注入：用 `systemctl start --no-block internal-image-studio-backup.service` 启动，等待 `/var/lib/internal-image-studio/worker-restart-required` 出现且 Worker 已停止，再执行 `systemctl kill --kill-who=main --signal=SIGKILL internal-image-studio-backup.service`。必须确认 `ExecStopPost` 把 Worker 恢复到 running/healthy、marker 被删除、`image_studio_backup_last_run_success` 变为 `0`，且本轮 snapshot 没有 verified tag；不可捕获信号留下的本轮 staging 必须在下一次持锁运行开始时被清除。再分别演练 Worker start 失败后 marker 保留、boot recovery 重试成功、Restic exit 3、并发第二次备份和 Worker 原本 stopped；任何路径都不能误恢复、误标 verified 或积累本地 dump。完成后 `systemctl reset-failed`，恢复 live 配置并重新成功执行备份；不要在有真实付费任务时做该演练。

备份与恢复脚本无论成功或失败，都会在退出 trap 中把“最近运行时间、最近运行是否成功、最近成功时间”写入同目录的临时文件，设置为 `0644` 后原子 rename 为 `cornfield_backup.prom` 或 `cornfield_restore_check.prom`；内容不含仓库地址、凭据、文件路径、用户数据或任务标识。失败不会覆盖之前的成功时间。指标写入异常会进入 systemd journal，而缺失/过期指标会由 Prometheus 告警。

Restic 仓库必须位于异机/对象存储；同机快照不能抵御磁盘和整机故障。每天检查 timer 与最新 snapshot，任何备份失败都应在下次发布前恢复。

## 恢复演练

`RESTORE_CHECK_ROOT` 必须是预先容量规划的专用恢复盘，且与系统根目录和生产 `DATA_ROOT` 位于不同文件系统；脚本会拒绝 `/tmp`、系统盘、生产资产盘、symlink 和非规范路径。它每月按精确 ID 恢复最新 verified Cornfield 快照。每轮先生成 128-bit invocation ID、原子写入持久 marker，再创建同 ID 的专用目录和带双 label 的一次性 PostgreSQL 容器；EXIT、`ExecStopPost` 与 boot recovery 都只清理 marker 指向且 label 匹配的资源，任何不匹配都 fail closed 并保留 marker。容器限制为 4GiB 内存、2 CPU，由 `studio_bootstrap` 预建 `studio_owner`、`studio_api`、`studio_worker`，把数据库/Schema owner 设为 `studio_owner`，再由非超管的 `studio_owner` 以 `--exit-on-error --single-transaction` 原样恢复 owner 与 ACL；这同时证明 archive 不依赖 superuser 才能恢复。演练会验证三个运行角色均非超管、数据库/Schema/对象 owner、API/Worker 的正负权限、所有未清理资产与 staged output 的文件引用均存在，并随机抽样核对 SHA-256；清理容器或精确临时目录失败也会把本轮标记为失败。它不挂载或修改生产 volume：

```bash
systemctl start internal-image-studio-backup.service
systemctl enable --now internal-image-studio-restore-check.timer
systemctl start internal-image-studio-restore-check.service
journalctl -u internal-image-studio-backup.service
journalctl -u internal-image-studio-restore-check.service
curl -fsS http://127.0.0.1:9090/api/v1/query?query=image_studio_backup_last_run_success
curl -fsS http://127.0.0.1:9090/api/v1/query?query=image_studio_restore_check_last_run_success
```

上线与每次变更后，两项 `last_run_success` 都必须为 `1`，并确认 `last_success_timestamp_seconds` 随成功执行推进。不要通过手工编辑 `.prom` 文件消除告警；应修复 systemd 任务并重新成功执行。

生产恢复必须单独评审恢复时间点和目标目录，先隔离损坏实例并停止所有写入，再恢复数据库与同一快照中的资产。全新 PostgreSQL 必须先由 `studio_bootstrap` 完成角色与 owner 引导，再以 `studio_owner` 在单事务中恢复保留的 owner/ACL；禁止使用 `--no-owner`、`--no-privileges` 或 superuser 恢复来掩盖权限问题。恢复后由 `studio_bootstrap` 只读审计最小权限角色，再依次运行 migration、`modelctl apply`、readiness、全部文件引用、随机资产 hash 和 mock/真实 canary。

## `submission_uncertain` 人工对账

`submission_uncertain` 表示上游 POST 已发出但客户端没有拿到可确认的 Provider job ID。系统会冻结该 job，不自动重提；普通 `/generations/{id}/retry` 只接受明确 `failed` 且 `retryable=true` 的 job，不能用于此状态。

收到 `ImageStudioSubmissionUncertain` 告警后：

1. 暂停对应 Provider 的新发布操作，保存 batch/job ID、时间窗口、Provider 和 model；不要取消或重试该 job。
2. 只读查询 `generation_jobs` 与 `provider_attempts`，用 `provider_request_id`、时间、模型和专用 Provider 控制台/API 搜索远端记录。不要用 prompt 作为唯一关联键，也不要在工单中粘贴 prompt 或签名 URL。
3. 若 Legnext 确认已接受且拿到远端 job ID，调用管理员 reconciliation 接口的 `attach_provider_job`，系统会绑定远端 ID 并只继续 poll/ingest，不重新 Submit：

   ```bash
   curl --fail-with-body --request POST \
     --cookie "$ADMIN_COOKIE" \
     --header "X-CSRF-Token: $CSRF" \
     --header 'Content-Type: application/json' \
     --data '{"action":"attach_provider_job","provider_job_id":"<remote-job-id>"}' \
     "$BASE_URL/api/v1/admin/jobs/<job-id>/reconcile-submission"
   ```

4. 只有 Provider 明确确认没有创建远端任务/产生同步结果时，才调用 `confirm_absent` 重新排队。响应会明确返回 `duplicate_cost_risk=true`；如果远端核查错误，仍可能重复计费：

   ```bash
   curl --fail-with-body --request POST \
     --cookie "$ADMIN_COOKIE" \
     --header "X-CSRF-Token: $CSRF" \
     --header 'Content-Type: application/json' \
     --data '{"action":"confirm_absent","confirmed_remote_absent":true}' \
     "$BASE_URL/api/v1/admin/jobs/<job-id>/reconcile-submission"
   ```

5. OpenRouter 图片请求同步返回、不能 Poll。若远端证据确认请求已接受/计费，但响应中的图片已经不可恢复，使用 `confirm_accepted_unrecoverable`；系统会把 job 终止为 `failed`、`retryable=false`，保留一次费用损失记录且永不重提：

   ```bash
   curl --fail-with-body --request POST \
     --cookie "$ADMIN_COOKIE" \
     --header "X-CSRF-Token: $CSRF" \
     --header 'Content-Type: application/json' \
     --data '{"action":"confirm_accepted_unrecoverable","confirmed_provider_accepted":true,"confirmed_result_unrecoverable":true}' \
     "$BASE_URL/api/v1/admin/jobs/<job-id>/reconcile-submission"
   ```

6. 每个动作会原子更新 job/batch、写 reconciliation/failed 事件和 `audit_logs`；外部工单仍应记录 Provider 证据、操作者和判断理由。确认 batch 汇总状态、输出唯一性、Provider 用量和事件一致后，再解除 Provider 发布暂停。

禁止直接手工 UPDATE 业务表，因为这会绕过状态机、事件、唯一性和审计。API 会拒绝对 OpenRouter 使用 `attach_provider_job`；不能为了消除告警而错误选择 `confirm_absent`，否则可能重复计费。

## 发布与回滚

### 发布

1. 在已通过主分支 CI 的 commit 上创建 `v*` Git tag。tag 与手工触发都不能绕过发布门：`release-images` 会先证明目标 SHA 可从 `origin/main` 到达，并通过 GitHub Actions API 确认同一 SHA 已有 `push/main` 的成功 `ci.yml` run；任一条件不满足即停止。随后 workflow 分别构建 API、Worker、Tools、Web 四个 `linux/amd64` OCI image，在没有 registry 写权限的 job 中验证 SBOM/provenance 并用 Trivy 阻断可修复的 HIGH/CRITICAL 漏洞，之后才把同一 OCI graph 发布到 `ghcr.io/<owner>/cornfield-{api,worker,tools,web}:<commit>`。
2. 下载该 workflow 的 `cornfield-image-digests-<commit>` artifact，在受控发布机验证：

   ```bash
   cd /secure/path/cornfield-image-digests-<commit>
   sha256sum -c SHA256SUMS
   test "$(sed -n 's/^RELEASE_COMMIT=//p' digests.env)" = "$(git -C /opt/internal-image-studio rev-parse HEAD)"
   test "$(grep -Ec '^(API|WORKER|TOOLS|WEB)_IMAGE=.+@sha256:[0-9a-f]{64}$' digests.env)" -eq 4
   ```

3. 以原子配置更新把 `digests.env` 中恰好四个 `*_IMAGE` 值写入部署 `.env`，保持 `RELEASE_REQUIRE_DIGESTS=true`；不要使用 commit tag 代替 digest。先安装/更新宿主 Nginx 配置和 TLS 文件，再用能读取证书私钥并执行完整 `nginx -t` 的账号（通常为 root）运行 preflight。通过后登录有 pull 权限的 GHCR，执行 `docker compose pull` 和 `docker compose up -d --no-build`。生产发布禁止 `--build`；`db-bootstrap`、`migrate` 和 `model-apply` 必须成功后 API/Worker 才启动。
4. 确认 CI、release security gate、最近备份/恢复演练、外部告警 receiver、Provider drift 均通过；发布前再执行一次备份。保存 Git tag/commit、原始 `digests.env`、`manifest.json`、完整 `docker compose config` 和上一版 artifact，作为确定性回滚输入。
5. 执行本手册的 health 命令，核对 `model_revision`、Worker heartbeat、Prometheus/Alertmanager targets、Provider 页面和磁盘空间。
6. 先用 mock/专用用户做内部 smoke，再人工执行最小真实 canary；观察至少一个完整任务周期、SSE 重连和资产下载后再宣布完成。

### 回滚

- 单机 Compose 没有零停机 HA；回滚会造成短暂不可用。先停止 Worker 接收新执行并记录所有 active/uncertain job，再用上一版已验证的 `digests.env` 原子替换四个镜像引用，执行 `docker compose pull` 与 `docker compose up -d --no-build`。API、Worker、Tools、Web 必须来自同一 release commit，禁止混用 tag 或本地镜像。
- 不自动回滚 PostgreSQL。数据库 migration 必须保持向后兼容；如果本次 migration 不兼容旧应用，必须在发布前准备并演练 forward-fix 或经评审的恢复方案，否则禁止发布。
- migration `009_cancelled_upstream_lease` 增加的 nullable 列对旧二进制是结构兼容的，但旧 Worker 不会把 `upstream_active_until` 计入并发或继续观察。只要存在 `upstream_active_until > now()` 的记录，就禁止回滚到不支持该租约的 Worker；应等待租约自然释放或发布 forward-fix，不能手工清空字段。
- 不删除 `model_capability_versions`。已入队任务通过 batch 的 `capability_revision` 使用不可变快照；回滚当前 catalog 时仍需运行对应版本的 `modelctl apply` 并等待 readiness 通过。
- 回滚后重复 health、Provider 和资产检查，确认队列继续推进；对发布窗口内的 `submission_uncertain` 逐一人工对账，不能因回滚而重新提交。
- 如果原因是数据损坏而非应用回归，停止写入并执行恢复流程，不要把应用回滚当作数据库/资产恢复。
