import { createFileRoute, Link } from '@tanstack/react-router'
import { ArrowLeft, Keyboard, MousePointer2 } from 'lucide-react'

import { AppShell } from '#/components/app-shell'

export const Route = createFileRoute('/app/director/help')({
  component: DirectorHelpPage,
})

const steps = [
  ['创建项目', '从导演台项目页建立一个空场景，项目会自动保存到当前账户。'],
  ['摆放人物与道具', '从模型库加入资源，使用 XYZ 三轴移动、旋转和缩放。'],
  ['记录镜头', '进入运镜工作台，用 WASD 移动，按 Enter 保存当前轨迹点。'],
  ['预演并发送', '切换摄影视角检查构图，将满意的截图发送到灵感墙。'],
]

export function DirectorHelpPage() {
  return (
    <AppShell>
      <main className="director-help-page">
        <Link to="/app/director" className="director-help-back">
          <ArrowLeft size={15} />
          返回导演台
        </Link>
        <header>
          <p className="eyebrow">QUICK START</p>
          <h1>从空间到镜头</h1>
          <p>四步完成第一次 3D 场景预演，不必预先掌握复杂的三维软件。</p>
        </header>
        <ol className="director-help-steps">
          {steps.map(([title, description], index) => (
            <li key={title}>
              <span>{index + 1}</span>
              <div>
                <strong>{title}</strong>
                <p>{description}</p>
              </div>
            </li>
          ))}
        </ol>
        <section className="director-help-grid">
          <article>
            <Keyboard size={18} />
            <h2>掌镜快捷键</h2>
            <dl>
              <div>
                <dt>W A S D</dt>
                <dd>前后左右移动</dd>
              </div>
              <div>
                <dt>Q / E</dt>
                <dd>下降 / 上升</dd>
              </div>
              <div>
                <dt>Enter</dt>
                <dd>保存当前镜头</dd>
              </div>
              <div>
                <dt>Space</dt>
                <dd>播放 / 暂停</dd>
              </div>
              <div>
                <dt>Esc</dt>
                <dd>退出掌镜</dd>
              </div>
            </dl>
          </article>
          <article>
            <MousePointer2 size={18} />
            <h2>鼠标与触控板</h2>
            <dl>
              <div>
                <dt>左键拖动</dt>
                <dd>环绕观察场景</dd>
              </div>
              <div>
                <dt>右键拖动</dt>
                <dd>平移观察中心</dd>
              </div>
              <div>
                <dt>滚轮 / 双指</dt>
                <dd>缩放场景或调整 FOV</dd>
              </div>
              <div>
                <dt>单击画面</dt>
                <dd>重新锁定掌镜视角</dd>
              </div>
            </dl>
          </article>
        </section>
        <section className="director-help-tools">
          <h2>主要区域</h2>
          <dl>
            <div>
              <dt>视口工具栏</dt>
              <dd>变换对象、导入模型、打开模型库与添加机位。</dd>
            </div>
            <div>
              <dt>镜头预设</dt>
              <dd>选择基础或高级预设，快速生成常见摄像机轨迹。</dd>
            </div>
            <div>
              <dt>右侧属性</dt>
              <dd>编辑对象参数、动作、人物路径与摄像机截图。</dd>
            </div>
            <div>
              <dt>底部时间轴</dt>
              <dd>播放、定位并调整场景与镜头的时间关系。</dd>
            </div>
          </dl>
        </section>
      </main>
    </AppShell>
  )
}
