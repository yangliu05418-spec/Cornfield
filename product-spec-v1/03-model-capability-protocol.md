# 03 · 模型能力协议

## 1. 目标

模型能力协议是 UI、参数校验、任务调度和供应商适配之间的唯一事实来源。新增模型应优先通过配置和能力同步完成，不修改生成页业务代码。

协议解决四类差异：

- 模型是否支持文生图、图生图以及最多几张参考图。
- 支持哪些比例、分辨率和抽卡范围。
- 一次抽卡返回几张图片，以及是否允许供应商原生批量。
- 内部标准参数如何映射到 Legnext prompt 或 OpenRouter JSON 字段。

## 2. 核心概念

| 概念 | 定义 |
|---|---|
| `model_id` | 内部稳定 ID，不直接依赖供应商模型名 |
| `provider_model_id` | 发给供应商的模型名或版本参数 |
| `draw` | 一次创作尝试/供应商生成任务 |
| `draw_count` | 用户要求的抽卡次数，V1 为 1～4 |
| `outputs_per_draw` | 每次抽卡预计输出数，可固定或为范围 |
| `capability_revision` | 能力快照版本；提交任务时固化，防止运行中变化 |
| `effective_capability` | 模型能力与具体供应商 endpoint 能力的交集 |

例：Midjourney 一次 diffusion 任务通常返回 4 个结果，因此 `draw_count=2` 的预期是 8 张；某 OpenRouter 模型一次请求默认返回 1 张，则预期是 2 张。

## 3. 协议定义（TypeScript 表达）

```ts
type Provider = "legnext" | "openrouter";
type GenerationMode = "text_to_image" | "image_to_image";
type Resolution = "MODEL_DEFAULT" | "512" | "1K" | "2K" | "4K";
type Ratio =
  | "auto" | "1:1" | "3:4" | "4:3" | "2:3" | "3:2"
  | "9:16" | "16:9" | "4:5" | "5:4" | "21:9";

interface ModelCapabilityV1 {
  schema_version: "model-capability/v1";
  model_id: string;                    // 例：mj-v8-1
  capability_revision: string;         // catalog 规范化内容的 SHA-256 hash
  enabled: boolean;

  provider: {
    type: Provider;
    provider_model_id: string;         // Legnext: midjourney；OR: org/model-slug
    endpoint_tag?: string;             // OpenRouter 可固定某个 endpoint
    source: "manual" | "provider_sync" | "provider_sync_reviewed";
    source_updated_at: string;
  };

  display: {
    name: string;
    short_name: string;
    description?: string;
    tags: string[];                     // 仅展示，例如：写实、文字表现
    sort_order: number;
  };

  input: {
    modes: GenerationMode[];
    prompt: { required: true; min_length: 1; max_length: number };
    references: {
      supported: boolean;
      min_items: number;
      max_items: number;
      accepted_mime_types: ("image/jpeg" | "image/png" | "image/webp")[];
      max_bytes_each: number;
      transport: "https_url" | "data_url" | "either";
      semantic: "image_prompt" | "edit_reference";
    };
  };

  controls: {
    aspect_ratio: {
      mode: "select" | "model_default";
      values: Ratio[];
      default: Ratio;
    };
    resolution: {
      mode: "select" | "model_default";
      values: Resolution[];
      default: Resolution;
    };
    draw_count: { min: 1; max: number; default: number };
    seed?: { supported: boolean; min?: number; max?: number };
  };

  output: {
    outputs_per_draw: { kind: "fixed"; value: number }
      | { kind: "range"; min: number; max: number; default: number };
    formats: ("png" | "jpeg" | "webp" | "svg")[];
    can_stream_preview: boolean;
    width_height_known_before_submit: boolean;
  };

  execution: {
    provider_async: boolean;
    supports_callback: boolean;
    supports_cancel: boolean;
    provider_native_batch?: { supported: boolean; max: number };
    timeout_seconds: number;
    max_attempts: number;
  };

  mapping: LegnextMapping | OpenRouterMapping;
}

interface LegnextMapping {
  adapter: "legnext.diffusion/v1";
  endpoint: "/api/v1/diffusion";
  version_prompt_suffix: string;        // 例：--v 8.1
  ratio_template: "--ar {{aspect_ratio}}";
  resolution_strategy: "model_version" | "prompt_suffix";
  reference_strategy: "prepend_urls_to_prompt";
}

interface OpenRouterMapping {
  adapter: "openrouter.images/v1";
  endpoint: "/api/v1/images";
  field_map: {
    aspect_ratio: "aspect_ratio";
    resolution: "resolution";
    references: "input_references";
    outputs: "n";
  };
  routing?: {
    only?: string[];
    order?: string[];
    allow_fallbacks?: boolean;
  };
}
```

## 4. Midjourney 示例

```json
{
  "schema_version": "model-capability/v1",
  "model_id": "mj-v8-1",
  "capability_revision": "7c1f...64-hex-capability-hash",
  "enabled": true,
  "provider": {
    "type": "legnext",
    "provider_model_id": "midjourney",
    "source": "manual",
    "source_updated_at": "2026-07-17T00:00:00Z"
  },
  "display": {
    "name": "Midjourney 8.1",
    "short_name": "MJ 8.1",
    "description": "审美稳定、适合概念与商业视觉",
    "tags": ["文生图", "图生图", "4张/次"],
    "sort_order": 10
  },
  "input": {
    "modes": ["text_to_image", "image_to_image"],
    "prompt": { "required": true, "min_length": 1, "max_length": 8192 },
    "references": {
      "supported": true,
      "min_items": 0,
      "max_items": 5,
      "accepted_mime_types": ["image/jpeg", "image/png", "image/webp"],
      "max_bytes_each": 10485760,
      "transport": "https_url",
      "semantic": "image_prompt"
    }
  },
  "controls": {
    "aspect_ratio": {
      "mode": "select",
      "values": ["1:1", "3:4", "4:3", "2:3", "3:2", "9:16", "16:9", "4:5", "5:4", "21:9"],
      "default": "1:1"
    },
    "resolution": {
      "mode": "model_default",
      "values": ["MODEL_DEFAULT"],
      "default": "MODEL_DEFAULT"
    },
    "draw_count": { "min": 1, "max": 4, "default": 1 },
    "seed": { "supported": false }
  },
  "output": {
    "outputs_per_draw": { "kind": "fixed", "value": 4 },
    "formats": ["png"],
    "can_stream_preview": false,
    "width_height_known_before_submit": false
  },
  "execution": {
    "provider_async": true,
    "supports_callback": true,
    "supports_cancel": false,
    "timeout_seconds": 600,
    "max_attempts": 2
  },
  "mapping": {
    "adapter": "legnext.diffusion/v1",
    "endpoint": "/api/v1/diffusion",
    "version_prompt_suffix": "--v 8.1",
    "ratio_template": "--ar {{aspect_ratio}}",
    "resolution_strategy": "model_version",
    "reference_strategy": "prepend_urls_to_prompt"
  }
}
```

注意：Legnext 文档允许在 prompt 中使用图片 URL 与 Midjourney 参数，但参考图上限等细节会随模型版本变化。示例值必须经过真实 Key 的冒烟测试后才能标为 `enabled=true`。

## 5. OpenRouter 示例

```json
{
  "schema_version": "model-capability/v1",
  "model_id": "or-seedream-4-5",
  "capability_revision": "7c1f...64-hex-capability-hash",
  "enabled": true,
  "provider": {
    "type": "openrouter",
    "provider_model_id": "bytedance-seed/seedream-4.5",
    "endpoint_tag": "bytedance",
    "source": "provider_sync_reviewed",
    "source_updated_at": "2026-07-17T00:00:00Z"
  },
  "display": {
    "name": "Seedream 4.5",
    "short_name": "Seedream",
    "description": "高分辨率通用图片生成",
    "tags": ["文生图", "图生图"],
    "sort_order": 20
  },
  "input": {
    "modes": ["text_to_image", "image_to_image"],
    "prompt": { "required": true, "min_length": 1, "max_length": 8000 },
    "references": {
      "supported": true,
      "min_items": 0,
      "max_items": 4,
      "accepted_mime_types": ["image/jpeg", "image/png", "image/webp"],
      "max_bytes_each": 10485760,
      "transport": "either",
      "semantic": "edit_reference"
    }
  },
  "controls": {
    "aspect_ratio": {
      "mode": "select",
      "values": ["1:1", "16:9", "9:16", "4:3", "3:4", "3:2", "2:3"],
      "default": "1:1"
    },
    "resolution": {
      "mode": "select",
      "values": ["1K", "2K", "4K"],
      "default": "2K"
    },
    "draw_count": { "min": 1, "max": 4, "default": 1 },
    "seed": { "supported": true, "min": 0, "max": 2147483647 }
  },
  "output": {
    "outputs_per_draw": { "kind": "fixed", "value": 1 },
    "formats": ["png", "jpeg", "webp"],
    "can_stream_preview": false,
    "width_height_known_before_submit": true
  },
  "execution": {
    "provider_async": false,
    "supports_callback": false,
    "supports_cancel": false,
    "provider_native_batch": { "supported": true, "max": 10 },
    "timeout_seconds": 300,
    "max_attempts": 2
  },
  "mapping": {
    "adapter": "openrouter.images/v1",
    "endpoint": "/api/v1/images",
    "field_map": {
      "aspect_ratio": "aspect_ratio",
      "resolution": "resolution",
      "references": "input_references",
      "outputs": "n"
    },
    "routing": {
      "only": ["bytedance"],
      "allow_fallbacks": false
    }
  }
}
```

该示例展示协议形状，不应替代在线能力同步。OpenRouter 的模型级能力是多个 endpoint 的并集；真正提交前必须使用被选 endpoint 的能力交集。

## 6. 能力同步规则

### 6.1 OpenRouter

1. `modelctl verify-remote` 只读获取 OpenRouter capability/endpoint 信息，与仓库中的静态模型 YAML 比较。
2. 对管理员允许的同模型 endpoint 计算 `effective_capability`；允许 OpenRouter 在这些 endpoint 间路由，但禁止静默切换成另一个模型。
3. 漂移只生成报告并显示最后验证时间，不自动修改、发现或上架模型。
4. 破坏性变化必须修改静态 YAML、通过代码评审和 `modelctl validate`，再随部署发布。
5. `modelctl apply` 以 capability hash 写入不可变 revision；API 使用进程内模型缓存，部署重启后加载新配置。已创建 batch/job 继续使用创建时的 revision，不需要外部分布式缓存失效或通用事件总线。

OpenRouter 的 `supported_parameters` 使用 `enum`、`range`、`boolean` 描述符；字段缺失即视为不支持。前端不得自行猜测通用比例或分辨率。

### 6.2 Legnext

Legnext 以 Midjourney prompt 参数映射为主，V1 采用人工维护 + 冒烟测试：

- 版本、比例、参考图、输出数量分别测试。
- 每周或收到供应商变更通知后重新运行。
- 未通过测试的能力不出现在用户界面。
- 模型版本切换新建内部 `model_id`，不静默替换旧版本。

## 7. 校验与兼容规则

- API 接收到任务后，按任务指定的 `capability_revision` 重新校验所有参数。
- 客户端 revision 落后时返回 409 `CAPABILITY_STALE`；客户端重新读取模型列表与最新 hash 后再提交。
- 切换模型时只迁移交集参数：prompt 永远保留；比例/分辨率不兼容则使用新模型默认值；参考图不兼容必须二次确认。
- `draw_count × outputs_per_draw.max` 不得超过全局单批输出上限 16。
- 不允许前端提交任意 provider-specific 参数。供应商透传选项只能由管理员配置进入能力协议。

## 8. 官方依据

- [OpenRouter Dedicated Image API](https://openrouter.ai/docs/guides/overview/multimodal/image-generation)：模型发现、endpoint 能力、`POST /api/v1/images`、`n`、分辨率、比例和参考图。
- [Legnext Text to Image](https://docs.legnext.ai/api-reference/image-generation/diffusion)：`POST /api/v1/diffusion`、prompt 长度、callback 与异步 Job。
- [Legnext Image Parameters](https://docs.legnext.ai/getting-started/image-parameters)：Midjourney prompt 参数与当前限制。
- [Legnext Model Information](https://docs.legnext.ai/getting-started/models)：版本能力与版本参数。
