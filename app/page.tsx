import Link from "next/link";

const competitions = [
  {
    name: "Nairobi Sunday Knockout",
    mode: "1v1 • Mobile",
    status: "Check-in opens 18:30",
    players: "48 / 64",
    accent: "lime",
  },
  {
    name: "Coast Rising Stars",
    mode: "1v1 • Mobile",
    status: "Registration open",
    players: "22 / 32",
    accent: "blue",
  },
  {
    name: "Gamics Open #01",
    mode: "Double elimination",
    status: "Starts Saturday",
    players: "91 / 128",
    accent: "orange",
  },
] as const;

const steps = [
  ["01", "Create your player card", "Claim a public profile and connect your eFootball identity."],
  ["02", "Enter a competition", "Register, check in and receive your match room instructions."],
  ["03", "Play and prove it", "Submit the result. Both players confirm, or an admin reviews evidence."],
] as const;

export default function Home() {
  return (
    <main>
      <header className="site-header shell">
        <Link className="brand" href="/" aria-label="Gamics home">
          <span className="brand-mark">G</span>
          <span>GAMICS</span>
        </Link>
        <nav aria-label="Primary navigation">
          <a href="#competitions">Competitions</a>
          <a href="#players">Players</a>
          <a href="#organizers">Organizers</a>
        </nav>
        <button className="button button-quiet" type="button">Join the arena</button>
      </header>

      <section className="hero shell">
        <div className="hero-copy">
          <p className="eyebrow"><span /> Built for African mobile esports</p>
          <h1>YOUR GAME.<br /><em>YOUR NAME.</em></h1>
          <p className="hero-lede">
            Enter trusted eFootball Mobile competitions, prove your skill and build a record that gets noticed.
          </p>
          <div className="hero-actions">
            <a className="button button-primary" href="#competitions">Find a competition <span>↗</span></a>
            <a className="text-link" href="#organizers">Run your own league <span>→</span></a>
          </div>
        </div>

        <div className="player-card-wrap" aria-label="Example Gamics player card">
          <div className="signal signal-one" />
          <div className="signal signal-two" />
          <article className="player-card">
            <div className="card-topline"><span>GAMICS PLAYER // KE</span><span className="live-dot">LIVE</span></div>
            <div className="player-avatar" aria-hidden="true"><span>KM</span></div>
            <div className="rank-pill">#024 KENYA</div>
            <h2>KIBERA<br />MAESTRO</h2>
            <p className="player-handle">@kibera.maestro</p>
            <div className="stat-grid">
              <div><strong>78%</strong><span>WIN RATE</span></div>
              <div><strong>41</strong><span>MATCHES</span></div>
              <div><strong>1,842</strong><span>RATING</span></div>
            </div>
            <div className="form-row"><span>RECENT FORM</span><b>W</b><b>W</b><b className="loss">L</b><b>W</b><b>W</b></div>
          </article>
        </div>
      </section>

      <section className="ticker" aria-label="Platform highlights">
        <div>LIVE BRACKETS <span>◆</span> VERIFIED RESULTS <span>◆</span> PLAYER RANKINGS <span>◆</span> FAIR COMPETITION <span>◆</span> MADE IN KENYA</div>
      </section>

      <section className="section shell" id="competitions">
        <div className="section-heading">
          <div><p className="eyebrow"><span /> Open now</p><h2>STEP INTO<br />THE BRACKET.</h2></div>
          <p>Start free. Build your record. Every match becomes part of your competitive story.</p>
        </div>
        <div className="competition-grid">
          {competitions.map((competition, index) => (
            <article className="competition-card" key={competition.name}>
              <div className={`competition-number ${competition.accent}`}>0{index + 1}</div>
              <div className="competition-content">
                <span className="game-label">eFOOTBALL™</span>
                <h3>{competition.name}</h3>
                <p>{competition.mode}</p>
                <div className="competition-meta"><span>{competition.status}</span><strong>{competition.players}</strong></div>
                <div className="capacity"><i style={{ width: `${48 + index * 14}%` }} /></div>
              </div>
              <button type="button" aria-label={`View ${competition.name}`}>↗</button>
            </article>
          ))}
        </div>
      </section>

      <section className="dark-section" id="players">
        <div className="shell">
          <div className="section-heading inverse">
            <div><p className="eyebrow"><span /> How it works</p><h2>SKILL LEAVES<br />A TRAIL.</h2></div>
            <p>Gamics turns informal matches into a trusted competitive record—from registration to the final whistle.</p>
          </div>
          <div className="step-grid">
            {steps.map(([number, title, body]) => (
              <article key={number}><span>{number}</span><h3>{title}</h3><p>{body}</p></article>
            ))}
          </div>
        </div>
      </section>

      <section className="organizer shell" id="organizers">
        <div>
          <p className="eyebrow"><span /> For organizers</p>
          <h2>RUN THE LEAGUE.<br /><em>WE RUN THE LOGIC.</em></h2>
        </div>
        <div className="organizer-copy">
          <p>Registration, check-in, brackets, result confirmation, evidence and disputes in one competition control room.</p>
          <button className="button button-dark" type="button">Become an organizer <span>↗</span></button>
        </div>
      </section>

      <footer className="shell">
        <div className="footer-row">
          <Link className="brand" href="/"><span className="brand-mark">G</span><span>GAMICS</span></Link>
          <p>Competition infrastructure for African esports.</p>
          <span>NAIROBI // 2026</span>
        </div>
        <p className="legal-note">eFootball is a trademark of Konami Digital Entertainment. Gamics is an independent platform and is not affiliated with or endorsed by Konami.</p>
      </footer>
    </main>
  );
}
