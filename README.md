# Cornfield

内部使用的文生图/图生图工作台。V1 集成 Legnext（Midjourney）和 OpenRouter（其他图片模型），不包含积分、支付、社区或 Draw 编辑能力。

## 已实现

- TanStack Start SPA：`/` 预渲染落地页，`/app/*` 静态 shell，无 Node 生产运行时。
- 高保真创作页：justified rows、行虚拟化、五档缩放、生成占位、逐 draw 取消、预览/参考/复制/下载。
- Go API：Argon2id 登录、opaque session、CSRF、用户管理、事务化 batch/job、SSE、流式上传和 X-Accel-Redirect。
- Go Worker：River、公平调度、Legnext/OpenRouter Adapter、mock provider、轮询/回调对账、模糊提交保护、数据库心跳、结果校验和 libvips 缩略图。
- PostgreSQL 18：业务真相、River 队列、可靠事件与 LISTEN/NOTIFY。
- 本地不可变资产：SHA-256 路径、原子提交、受保护直出、90 天清理、孤儿扫描和磁盘压力保护。
- 不可变模型能力快照：任务始终按创建时的 capability revision 执行，模型配置更新不会改变已排队任务。
- Docker Compose：四个长期服务；迁移和模型应用是一次性 job；监控使用可选 profile。

## 本地验证

```powershell
cd backend
& 'C:\Program Files\Go\bin\go.exe' test ./...
& 'C:\Program Files\Go\bin\go.exe' vet ./...
& 'C:\Program Files\Go\bin\go.exe' run ./cmd/modelctl validate

cd ..\web
pnpm install --frozen-lockfile
pnpm check
pnpm typecheck
pnpm lint
pnpm test
pnpm build
pnpm exec playwright install chromium
pnpm e2e
```

CI 还会执行 `go test -race`、纯 Go 二进制构建、生产镜像构建、Compose 网络与权限约束检查，并在 fresh PostgreSQL 上运行 `ops/ci-smoke.sh`。该 smoke 以 `PROVIDER_MODE=mock` 覆盖最小权限数据库角色、登录、图像上传与验证、图生图、幂等重放、Worker、资产响应和 SSE，不消耗真实额度；它只允许在一次性 CI runner/disposable checkout 中执行，不能在保存真实 secret 的生产 checkout 中运行。真实模式由 Compose 默认启用。

## Docker Compose 部署

1. 将仓库部署到 `/opt/internal-image-studio`，复制 `.env.example` 为 `.env`，设置真实 HTTPS URL。`DATA_ROOT` 固定为 `/srv/internal-image-studio/data`，必须与宿主 Nginx 和备份配置一致。生产部署从 GitHub release workflow 下载 `cornfield-image-digests-<commit>`，校验其中的 `SHA256SUMS` 和 `RELEASE_COMMIT`，再把 `digests.env` 的四个 `*_IMAGE` 引用写入 `.env`；它们必须全部使用 `@sha256:` 固定。
2. 创建以下文件；每个文件只放一行 secret 本身，不要带引号：
   - `secrets/postgres_bootstrap_password`
   - `secrets/postgres_owner_password`
   - `secrets/postgres_api_password`
   - `secrets/postgres_worker_password`
   - `secrets/legnext_api_key`
   - `secrets/openrouter_api_key`
   - `secrets/provider_callback_secret`
   - `secrets/provider_url_signing_secret`
   - `secrets/grafana_admin_password`（仅监控 profile 需要）
   - `secrets/alertmanager_webhook_url`（仅告警 profile 需要，内容为单行 HTTPS webhook URL）

   四个数据库密码分别生成、互不复用，每个至少 32 个无空白字符；`studio_bootstrap` 只用于数据库引导，migration/model apply 使用 `studio_owner`，API 与 Worker 使用各自的最小权限角色。两个 Provider key 从用户提供的原始文件中只提取 token 本身；不要把说明文字、引号或 Markdown 一并写入 secret。两个内部签名 secret 分别使用 `openssl rand -base64 48` 生成，不能复用。除监控 secret 外，上述 secret 源文件须由 UID/GID `65532:65532` 持有并设为 `0600`；Grafana secret 使用 `472:472`，Alertmanager webhook secret 使用 `65534:65534`，两者同样设为 `0600`。Compose 的 file-backed secret 是 bind mount，不能依赖容器内 UID remap。数据库密码不得写入 `.env`、`DATABASE_URL`、命令行或 `.pgpass`。

3. 在宿主机建立与容器数值 GID 一致的只读共享组，并显式创建每一层数据目录。不要只把叶子目录交给 `install -d`：GNU `install` 会把缺失的中间目录按 `root:root 0755` 创建，导致 preflight 或运行时权限失败。下面的 `cornfield-runtime` 必须占用 GID `65532`；如果该 GID 已被其他用途占用，先完成主机身份规划，不要复用一个无关的权限域。

   ```bash
   getent group 65532 >/dev/null || groupadd --gid 65532 cornfield-runtime
   test "$(getent group 65532 | cut -d: -f1)" = "cornfield-runtime"
   install -d -o 65532 -g 65532 -m 0750 \
     /srv/internal-image-studio/data \
     /srv/internal-image-studio/data/assets
   install -d -o 65532 -g 65532 -m 0700 \
     /srv/internal-image-studio/data/uploads \
     /srv/internal-image-studio/data/uploads/tmp \
     /srv/internal-image-studio/data/uploads/quarantine
   ```

4. 先安装宿主 Nginx，将 [站点配置](ops/nginx/internal-image-studio.conf) 安装到 `.env` 的 `NGINX_SITE_CONFIG` 指定路径，并替换唯一域名与证书/私钥路径。设置 `NGINX_WORKER_USER` 为宿主 `nginx.conf` 的实际 `user`（Debian/Ubuntu 通常是 `www-data`），将该 worker 账号加入 `cornfield-runtime` 后重启 Nginx，使已经运行的 worker 真正继承 GID `65532`：

   ```bash
   usermod -aG cornfield-runtime www-data
   systemctl restart nginx
   ```

   正式资产目录/文件由 Worker 以 GID `65532` 和只读组权限写入；Nginx 不加入该组会让 `X-Accel-Redirect` 返回 `403`。Compose 将 API 的整个 `/data` 挂为只读，仅用嵌套挂载开放 `/data/uploads` 写权限；只有 Worker 能写完整数据树，不要把 API 的父挂载改成可写。上传目录使用 `0700`，因此加入共享组不会让 Nginx 读取 quarantine。`APP_PUBLIC_URL` 必须是标准 `443` 端口、无账号、路径、query 或 fragment 的 bare HTTPS origin，其 host 必须与配置中的 `server_name` 完全一致。Callback 与 Provider asset location 必须保持关闭 access log；`/_protected_assets/` 必须保持 `internal`。
5. 在 `.env` 中设置 `RELEASE_REQUIRE_DIGESTS=true`。以 root 运行 `chmod +x ops/*.sh && STUDIO_ROOT=/opt/internal-image-studio ops/preflight.sh`；preflight 会验证 UID/GID/mode、以 UID `65532` 实测目录可写、检查完整资产树权限、确认 Nginx worker 的账号与当前进程都已继承 GID `65532`，并要求 `NGINX_SITE_CONFIG` 指向的文件与仓库审查版本逐字一致且确实出现在 `nginx -T` 的活动配置中。它还会读取 TLS 文件并执行完整配置测试，因此必须在第 4 步安装、重启 Nginx 后运行。通过后执行 `docker compose pull` 与 `docker compose up -d --no-build`。检查会拒绝未固定 digest 的四个应用镜像、缺失或权限错误的 secret、错误的 HTTPS origin/Nginx/TLS 配置，以及允许内联脚本的 CSP。`db-bootstrap`、migration 和 model apply 必须成功退出后，API/Worker 才会启动。
6. 创建首个管理员：

```bash
read -r -s -p "Admin password: " CORNFIELD_ADMIN_PASSWORD; printf '\n'
printf '%s\n' "$CORNFIELD_ADMIN_PASSWORD" | docker compose run --rm -T --no-deps model-apply \
  adminctl --username admin --display-name "Studio Admin"
unset CORNFIELD_ADMIN_PASSWORD
```

开启监控：

```bash
docker compose --profile observability up -d
```

仅启用 observability profile 会评估规则但不会向外发送通知。配置并演练外部 HTTPS receiver 后，再启用告警：

```bash
docker compose --profile observability --profile alerting up -d
curl -fsS http://127.0.0.1:9093/-/ready
```

详细的 health/readiness、真实 Provider canary 边界、备份恢复、告警、发布回滚和 `submission_uncertain` 对账见 [运维手册](docs/OPERATIONS.md)。100 RPS 批次创建与 200 SSE 会话脚本见 [负载测试说明](ops/load/README.md)。产品和接口基线仍保存在 [product-spec-v1](product-spec-v1/README.md)。

## 关键边界

- API 不执行生成、轮询、下载或缩略图任务。
- Provider Adapter 不写业务表。
- 前端不读取 River 表。
- Callback 只唤醒对账，不作为结果真相。
- 上游 POST 超时且无法确认接受时进入 `submission_uncertain`，不会自动重复计费。
- 对不能真正取消的上游，用户主动取消会尽快进入 `cancelled`。未取消任务到达生成 deadline 时，Worker 会先做一次认证 final poll，尽量恢复刚完成的已付费结果；若远端仍未终止且取消未被接受，任务进入 `failed`。两种场景都可通过 `upstream_active_until` 在远端终态或“原 generation deadline + 有界观察宽限”前继续占用用户/Provider/模型并发；宽限通常等于模型生成 timeout，最多 1 小时。Worker 只做后台观察并丢弃取消任务的迟到结果；支持真实取消且已接受取消的上游不会保留该租约。
- API key、密码、base64 图片和文件系统绝对路径禁止进入日志。
- 宿主 Nginx 通过仅绑定 `127.0.0.1` 的端口访问 Web/API，因此 `frontend` 必须是宿主可达的普通 bridge；默认 host binding 也固定为 loopback。Worker 通过独立 egress 网络访问 Provider；PostgreSQL 始终只连接 internal backend 网络且不发布端口。
