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
          <a href="#studio">Studio</a>
          <a href="#system">System</a>
          <a href="#principles">Principles</a>
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
          CORNFIELD / PRIVATE IMAGE STUDIO
        </p>
        <h1>
          Make images.
          <br />
          Keep momentum.
          <br />
          <em>Stay in flow.</em>
        </h1>
        <div className="landing-meta">
          <p>
            ONE PLACE
            <br />
            FOR EVERY MODEL
          </p>
          <p>
            TEXT → IMAGE
            <br />
            IMAGE → IMAGE
          </p>
          <p>
            A high-throughput studio for teams who care about the work, not the
            wait.
          </p>
          <Link to="/app/login" className="landing-enter">
            ENTER STUDIO <ArrowUpRight size={13} />
          </Link>
        </div>
      </section>
      <section className="landing-field" id="system">
        <div className="field-index">SYSTEM / ALWAYS READY</div>
        <h2>
          ONE WALL.
          <br />
          EVERY IDEA.
        </h2>
        <p>The queue disappears. The work remains.</p>
        <ArrowDownRight className="field-arrow" size={42} strokeWidth={1} />
      </section>
      <section className="landing-principles" id="principles">
        <p className="landing-kicker">
          <span />
          BUILT FOR INTERNAL VELOCITY
        </p>
        <div className="principle-grid">
          <article>
            <span>01</span>
            <h3>Fast by default</h3>
            <p>
              Local assets, direct delivery, and a wall that stays fluid at
              scale.
            </p>
          </article>
          <article>
            <span>02</span>
            <h3>Failure is visible</h3>
            <p>
              Every draw has a state. Every upstream failure has a safe path
              home.
            </p>
          </article>
          <article>
            <span>03</span>
            <h3>Tools stay quiet</h3>
            <p>
              No points, plans, feeds, or noise. Just model, ratio, resolution,
              and draw.
            </p>
          </article>
        </div>
      </section>
      <footer className="landing-footer">
        <span>CORNFIELD</span>
        <span>HONG KONG / 2026</span>
        <Link to="/app/login">OPEN WORKSPACE →</Link>
      </footer>
    </main>
  )
}
