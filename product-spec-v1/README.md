# Internal Image Studio · V1 规格索引

版本：V1.0 Draft

日期：2026-07-17

范围：内部图片生成工具，不含商业化、积分、Draw/局部绘制与视频能力。

## 决策摘要

- 产品核心是“生成 → 浏览 → 复用为参考图 → 再生成”的连续创作闭环。
- 首页即图片生成页；最近生成内容直接构成可缩放的瀑布流灵感墙。
- 统一使用“抽卡次数（draw count）”描述提交次数，不把它误写成图片数。最终图片数由模型的 `outputs_per_draw` 决定。
- Legnext 负责 Midjourney，OpenRouter Dedicated Image API 负责其他模型；两者都封装为内部异步任务。
- 前端只渲染模型能力协议声明的控件，禁止按供应商名称写条件分支。
- API Key 只存在于服务端密钥系统中，浏览器永远不接触供应商凭证。

## 文档清单

1. [信息架构与页面清单](./01-information-architecture.md)
2. [图片生成页高保真线框](./02-image-generation-wireframe.md)
3. [模型能力协议](./03-model-capability-protocol.md)
4. [Legnext / OpenRouter 统一任务接口](./04-unified-generation-api.md)
5. [MVP PRD 与数据库模型](./05-mvp-prd-and-data-model.md)

## 评审顺序

建议按 1 → 2 → 3 → 4 → 5 评审。前两份锁定用户体验，后三份锁定模型接入和工程边界。
