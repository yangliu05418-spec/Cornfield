import { createFileRoute, Link } from '@tanstack/react-router'
import { ArrowRight, Play, Search } from 'lucide-react'

export const Route = createFileRoute('/')({ component: LandingPage })

const floatingFrames = [
  ['horizon', '远处地平线上的光'],
  ['figure', '巨大空间中的一人'],
  ['fabric', '暗处悬浮的织物'],
  ['water', '黑色水面上的微光'],
  ['grass', '风中的旷野'],
  ['room', '通向光的房间'],
] as const

const visions = [
  {
    image: 'fabric',
    title: '从一句未说完的话开始',
    copy: '文字给出方向，图像寻找它尚未被看见的形状。',
  },
  {
    image: 'figure',
    title: '让一张图像继续生长',
    copy: '把已有的画面交给下一次想象，不必回到原点。',
  },
  {
    image: 'grass',
    title: '把灵感留在同一片视野',
    copy: '生成、观看与整理在一面墙上发生，让工具退到远处。',
  },
] as const

function DotMark() {
  return (
    <span className="landing-dot-mark" aria-hidden="true">
      {Array.from({ length: 9 }, (_, index) => (
        <i key={index} />
      ))}
    </span>
  )
}

function LandingPage() {
  return (
    <main className="landing landing-v2">
      <header className="landing-nav">
        <Link to="/" className="landing-wordmark" aria-label="Cornfield 首页">
          <DotMark />
          <span>Cornfield</span>
        </Link>

        <nav aria-label="公开导航">
          <a href="#film">关于</a>
          <a href="#visions">创作方式</a>
        </nav>

        <a className="landing-search" href="#visions">
          <Search size={16} strokeWidth={1.5} aria-hidden="true" />
          <span>探索图像的下一种可能</span>
        </a>

        <div className="landing-nav-actions">
          <span>PRIVATE / 2026</span>
          <Link to="/app/login" className="landing-nav-login">
            登录
          </Link>
          <Link to="/app/login" className="landing-nav-enter">
            进入工作室
          </Link>
        </div>
      </header>

      <section className="landing-hero" aria-labelledby="landing-title">
        <div className="landing-collage" aria-hidden="true">
          {floatingFrames.map(([image, alt]) => (
            <img
              key={image}
              className={`landing-tile landing-tile-${image}`}
              src={`/cornfield-${image}.webp`}
              alt={alt}
            />
          ))}
        </div>

        <div className="landing-hero-copy">
          <p className="landing-eyebrow">CORNFIELD / 未来影像工作室</p>
          <h1 id="landing-title">
            把尚未发生的图像，
            <br />
            带到眼前。
          </h1>
          <p className="landing-intro">
            有些画面先于语言存在。我们为它留出一片安静的场域——让模型彼此靠近，
            让想象不必等待。
          </p>
          <div className="landing-hero-actions">
            <Link to="/app/login" className="landing-primary-button">
              进入工作室 <ArrowRight size={17} strokeWidth={1.5} />
            </Link>
            <a href="#film" className="landing-secondary-button">
              <Play size={14} fill="currentColor" strokeWidth={0} />
              看一束光如何抵达
            </a>
          </div>
        </div>
      </section>

      <section className="landing-film" id="film" aria-labelledby="film-title">
        <div className="landing-section-heading">
          <p>时间经过，图像留下</p>
          <span>01 / 03</span>
        </div>
        <div className="landing-film-frame">
          <img src="/cornfield-horizon.webp" alt="暮色下延伸至地平线的田野" />
          <div className="landing-film-scrim" aria-hidden="true" />
          <div className="landing-film-title" id="film-title">
            <span>观看</span>
            <Play size={24} fill="currentColor" strokeWidth={0} />
            <span>一束光的来处</span>
          </div>
          <p>关于时间、想象，以及尚未抵达的图像</p>
          <DotMark />
        </div>
      </section>

      <section
        className="landing-visions"
        id="visions"
        aria-labelledby="visions-title"
      >
        <div className="landing-section-heading">
          <h2 id="visions-title">创作不是抵达，是持续看见。</h2>
          <span>02 / 03</span>
        </div>
        <div className="landing-vision-grid">
          {visions.map((vision) => (
            <article key={vision.title}>
              <img
                src={`/cornfield-${vision.image}.webp`}
                alt=""
                loading="lazy"
              />
              <h3>{vision.title}</h3>
              <p>{vision.copy}</p>
            </article>
          ))}
        </div>
      </section>

      <section className="landing-closing" aria-labelledby="closing-title">
        <p>献给仍愿意凝视的人</p>
        <h2 id="closing-title">
          先让光抵达。
          <br />
          再让意义发生。
        </h2>
        <Link to="/app/login" className="landing-primary-button">
          开始创作 <ArrowRight size={17} strokeWidth={1.5} />
        </Link>
      </section>

      <footer className="landing-footer">
        <Link to="/" className="landing-wordmark">
          <DotMark />
          <span>Cornfield</span>
        </Link>
        <p>一间安静、私有、始终在场的图像工作室。</p>
        <span>HONG KONG / 2026</span>
      </footer>
    </main>
  )
}
