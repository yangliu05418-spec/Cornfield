import { createFileRoute, Link } from '@tanstack/react-router'
import { ArrowRight, Play, Search } from 'lucide-react'
import type { CSSProperties } from 'react'

export const Route = createFileRoute('/')({ component: LandingPage })

const floatingFrames = [
  ['horizon', 156, 1.32, -5, 0.82],
  ['solitude', 82, 0.72, 4, 0.58],
  ['curtain', 108, 0.88, -2, 0.9],
  ['black-cube', 92, 1, 3, 0.46],
  ['red-field', 132, 1.42, -4, 0.78],
  ['tree', 84, 0.92, 3, 0.5],
  ['passage', 132, 1.48, 2, 0.86],
  ['figure', 86, 0.72, -3, 0.56],
  ['doors', 122, 1.36, 4, 0.76],
  ['fabric', 102, 0.88, -3, 0.72],
  ['chair', 138, 1.44, -2, 0.9],
  ['room', 82, 0.72, 4, 0.48],
  ['grass', 118, 1.28, -3, 0.8],
  ['water', 84, 0.78, 3, 0.52],
  ['red-field', 140, 1.42, 2, 0.76],
  ['solitude', 80, 0.72, -4, 0.4],
  ['tree', 106, 0.92, 2, 0.72],
  ['curtain', 86, 0.88, -3, 0.4],
  ['horizon', 138, 1.32, 4, 0.76],
  ['black-cube', 84, 1, -2, 0.46],
  ['passage', 136, 1.48, 3, 0.76],
  ['figure', 86, 0.72, -4, 0.56],
  ['doors', 116, 1.36, 3, 0.84],
  ['fabric', 80, 0.88, -2, 0.42],
  ['chair', 74, 1.44, 4, 0.26],
  ['water', 70, 0.78, -3, 0.24],
  ['tree', 62, 0.92, 2, 0.18],
  ['black-cube', 64, 1, -2, 0.16],
  ['grass', 70, 1.28, 3, 0.2],
  ['curtain', 68, 0.88, -3, 0.18],
] as const

const orbitRings = [
  { x: '47%', y: '43%', duration: 38 },
  { x: '42%', y: '37%', duration: 34 },
  { x: '37%', y: '32%', duration: 31 },
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

function CubeMark() {
  return <img className="landing-cube-mark" src="/cornfield-cube.svg" alt="" />
}

function LandingPage() {
  return (
    <main className="landing landing-v2">
      <header className="landing-nav">
        <Link to="/" className="landing-wordmark" aria-label="Cornfield 首页">
          <CubeMark />
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
          {floatingFrames.map(
            ([image, width, ratio, rotation, opacity], index) => (
              <span
                key={`${image}-${index}`}
                className="landing-orbit-item"
                style={
                  {
                    '--tile-width': `${width}px`,
                    '--tile-ratio': ratio,
                    '--tile-rotation': `${rotation}deg`,
                    '--tile-opacity': opacity,
                    '--orbit-x': orbitRings[index % orbitRings.length].x,
                    '--orbit-y': orbitRings[index % orbitRings.length].y,
                    '--orbit-start': `${(index / floatingFrames.length) * 100}%`,
                    '--orbit-duration': `${orbitRings[index % orbitRings.length].duration}s`,
                    '--tile-delay': `${index * 32}ms`,
                  } as CSSProperties
                }
              >
                <img
                  className="landing-tile"
                  src={`/cornfield-${image}.webp`}
                  alt=""
                />
              </span>
            ),
          )}
        </div>

        <div className="landing-hero-copy">
          <p className="landing-eyebrow">CORNFIELD / 未来影像工作室</p>
          <h1 id="landing-title">
            <span>让想象先于现实。</span>
            <span>让图像抵达眼前。</span>
          </h1>
          <div className="landing-hero-actions">
            <Link to="/app/login" className="landing-primary-button">
              进入工作室 <ArrowRight size={17} strokeWidth={1.5} />
            </Link>
            <a href="#film" className="landing-secondary-button">
              探索创作方式
            </a>
          </div>
        </div>
        <a href="#film" className="landing-film-link">
          <Play size={12} fill="currentColor" strokeWidth={0} />
          观看一束光如何抵达
        </a>
      </section>

      <section className="landing-film" id="film" aria-labelledby="film-title">
        <div className="landing-section-heading">
          <p>时间经过，图像留下</p>
          <span>01 / 03</span>
        </div>
        <div className="landing-film-frame">
          <img
            className="landing-film-image"
            src="/cornfield-horizon.webp"
            alt="暮色下延伸至地平线的田野"
          />
          <div className="landing-film-scrim" aria-hidden="true" />
          <div className="landing-film-title" id="film-title">
            <span>观看</span>
            <Play size={24} fill="currentColor" strokeWidth={0} />
            <span>一束光的来处</span>
          </div>
          <p>关于时间、想象，以及尚未抵达的图像</p>
          <CubeMark />
        </div>
      </section>

      <section
        className="landing-visions"
        id="visions"
        aria-labelledby="visions-title"
      >
        <div className="landing-section-heading">
          <h2 id="visions-title">
            <span>创作不是抵达。</span>
            <span>是持续看见。</span>
          </h2>
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
          <span>先让光抵达。</span>
          <span>再让意义发生。</span>
        </h2>
        <Link to="/app/login" className="landing-primary-button">
          开始创作 <ArrowRight size={17} strokeWidth={1.5} />
        </Link>
      </section>

      <footer className="landing-footer">
        <Link to="/" className="landing-wordmark">
          <CubeMark />
          <span>Cornfield</span>
        </Link>
        <p>一间安静、私有、始终在场的图像工作室。</p>
        <span>HONG KONG / 2026</span>
      </footer>
    </main>
  )
}
