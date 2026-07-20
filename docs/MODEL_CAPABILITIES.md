# 模型能力边界

本表记录 Cornfield 当前启用模型的生产能力上限。OpenRouter 模型以其 Dedicated Image API 的实时 endpoint discovery 为准；部署前由 `modelctl verify-remote` 检测漂移。日期：2026-07-20。

| Cornfield 模型 | 参考图上限 | Cornfield 单图上限 | 边界说明 |
|---|---:|---:|---|
| Midjourney V8.1 / V7（Legnext） | 4 | 10 MiB | Cornfield 的保守产品上限。Legnext 与 Midjourney 文档允许多个 image prompt，但未公布可验证的数字硬上限；扩大前必须做付费 canary。 |
| Nano Banana 2 Lite | 14 | 25 MiB | OpenRouter 的 Google AI Studio 与 Vertex endpoints 均声明 `input_references.max=14`。 |
| Nano Banana 2 | 14 | 25 MiB | OpenRouter 两个 Google endpoints 均为 14；Google 还区分人物/物体的高保真保持能力，因此 14 不等于 14 个独立身份都能等质量复现。 |
| Nano Banana Pro | 14 | 25 MiB | OpenRouter 两个 Google endpoints 均为 14；Cornfield 的分辨率仍取多 endpoint 的安全交集。 |
| GPT Image 2 | 16 | 25 MiB | OpenRouter OpenAI endpoint 声明 `input_references.max=16`。画质可传 `quality=auto/low/medium/high`；该 endpoint 不声明比例或分辨率字段。 |
| Grok Imagine Image Quality | 3 | 25 MiB | OpenRouter 与 xAI 多图编辑文档的上限一致。 |
| Seedream 4.5 | 14 | 25 MiB | OpenRouter Seed endpoint 声明 14。上游常见约束是输入图与输出图合计最多 15；Cornfield 每 draw 请求 1 张输出，因此最多开放 14 张输入。 |
| FLUX.2 Max（BFL 官方） | 8 | 25 MiB | 官方 API 上限为 8；Playground 的 10 张不属于 API 合约。输出最大 4MP、边长至少 64px、尺寸为 16 的倍数。 |

实现规则：

- API 必须按 capability revision 校验数量和单图字节数；Provider Adapter 再做一次最终上限校验。
- 单图字节数是 Cornfield 上传层的防护值，不代表 Provider 承诺接收该体积；真实可用性还受解码后像素、格式和上游抓取限制影响。
- 参考图通过短期、同源、签名 URL 交给异步 Provider，正式资产权限不公开；结果必须在临时 URL 失效前复制到本地不可变存储。
- 数量上限只表示请求可被接收，不保证多主体身份、文字、姿态和风格都能同等准确保留。多参考场景的 prompt 应明确说明每张图的用途。
- 不根据 Playground、营销页或单次成功请求自动扩大生产上限。远端能力发生变化时，先产生 drift 报告，再经代码评审和 canary 更新静态配置。

主要资料：

- [OpenRouter Unified Image API](https://openrouter.ai/docs/guides/overview/multimodal/image-generation)
- [OpenRouter image endpoint discovery](https://openrouter.ai/blog/announcements/image-api/)
- [BFL FLUX.2 overview](https://docs.bfl.ai/flux_2/flux2_overview)
- [BFL FLUX.2 Max API](https://docs.bfl.ai/api-reference/models/generate-or-edit-an-image-with-flux2-%5Bmax%5D)
- [Google image generation](https://ai.google.dev/gemini-api/docs/image-generation)
- [xAI multi-image editing](https://docs.x.ai/developers/model-capabilities/images/multi-image-editing)
- [Legnext model information](https://docs.legnext.ai/getting-started/models)
- [Midjourney image prompts](https://docs.midjourney.com/hc/en-us/articles/32040250122381-Image-Prompts)
