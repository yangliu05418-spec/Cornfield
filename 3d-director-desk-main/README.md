# Cornfield 3D Director Desk

React 18 + React Three Fiber + Three.js 子应用。生产环境由 Cornfield Web 镜像构建到 `/director-desk/`，并通过同源 iframe 从 `/app/director/:projectId` 加载；不要把它与 Cornfield React 19 主应用合包。

## 本地开发

```bash
npm ci
npm run dev
```

独立开发模式保留本机工程列表。Cornfield 嵌入协议通过以下查询参数启用：

```text
/?embedded=1&hostOrigin=http://localhost:3000&instanceId=<project-id>
```

## 验证

```bash
npm test
npm run build:embedded
```

`build:embedded` 只发布主编辑器入口，不包含 `examples/experiments` 下的实验页面。云端工程保存场景数据和内置模型引用；本地导入模型的二进制仍只保存在当前设备。素材授权边界见 [ASSET_SOURCES.md](./ASSET_SOURCES.md)。
