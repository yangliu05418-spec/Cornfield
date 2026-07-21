import { createFileRoute, Link } from '@tanstack/react-router'
import { ArrowDownRight, ArrowUpRight } from 'lucide-react'

export const Route = createFileRoute('/')({ component: LandingPage })

function LandingPage() {
  return (
    <main className="landing">
      <header className="landing-nav">
        <Link to="/" className="landing-wordmark">
          <span aria-hidden="true" />
          Cornfield
        </Link>
        <nav aria-label="公开导航">
          <a href="#studio">工作室</a>
          <a href="#system">创作系统</a>
          <a href="#principles">设计原则</a>
        </nav>
        <div className="landing-counter">
          <span />
          PRIVATE / 2026
        </div>
      </header>
      <section className="landing-hero" id="studio">
        <div className="orbital-line" aria-hidden="true" />
        <div className="field-horizon" aria-hidden="true" />
        <p className="landing-kicker">
          <span />
          CORNFIELD / 私有图像工作室
        </p>
        <h1>
          让图像发生。
          <br />
          让灵感持续。
          <br />
          <em>保持创作流动。</em>
        </h1>
        <div className="landing-meta">
          <p>
            一个工作台
            <br />
            连接所有模型
          </p>
          <p>
            文字生成图像
            <br />
            图像启发图像
          </p>
          <p>为专注作品而非等待的团队，提供快速、稳定、持续流动的创作体验。</p>
          <Link to="/app/login" className="landing-enter">
            进入工作室 <ArrowUpRight size={13} />
          </Link>
        </div>
      </section>
      <section className="landing-field" id="system">
        <div className="field-index">创作系统 / 随时就绪</div>
        <h2>
          一面灵感墙。
          <br />
          承接每个想法。
        </h2>
        <p>让队列退到幕后，让作品留在眼前。</p>
        <ArrowDownRight className="field-arrow" size={42} strokeWidth={1} />
      </section>
      <section className="landing-principles" id="principles">
        <p className="landing-kicker">
          <span />
          为团队创作效率而生
        </p>
        <div className="principle-grid">
          <article>
            <span>01</span>
            <h3>生来迅捷</h3>
            <p>本地资产直出，灵感墙在大量图片下依然流畅。</p>
          </article>
          <article>
            <span>02</span>
            <h3>状态清晰可见</h3>
            <p>每次生成都有明确状态，每次上游异常都有安全的恢复路径。</p>
          </article>
          <article>
            <span>03</span>
            <h3>工具保持安静</h3>
            <p>没有积分、套餐和信息噪音，只有模型、比例、画质与创作。</p>
          </article>
        </div>
      </section>
      <footer className="landing-footer">
        <span>CORNFIELD</span>
        <span>HONG KONG / 2026</span>
        <Link to="/app/login">进入工作室 →</Link>
      </footer>
    </main>
  )
}
