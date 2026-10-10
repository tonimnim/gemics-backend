import Link from "next/link";
import type { CSSProperties } from "react";
import { BrandMark } from "./brand-mark";
import { CompetitionCard } from "./competition-card";
import { AndroidIcon, ArrowIcon, ControllerIcon, PhoneIcon, ShieldCheckIcon, TicketIcon } from "./icons";
import { type Competition, formatLabel, loadCompetitions } from "./lib/competitions";
import { SiteFooter, SiteHeader } from "./site-chrome";

const openStatuses = new Set(["registration_open", "check_in"]);

const steps = [
  { title: "Enter", body: "Pick a tournament and lock in your slot.", Icon: TicketIcon },
  { title: "Play", body: "Meet your opponent in eFootball and play it out.", Icon: ControllerIcon },
  { title: "Report", body: "One of you submits the score. The other confirms it.", Icon: ShieldCheckIcon },
];

const fallbackTape = ["eFootball Mobile", "Knockout", "League", "Double elimination"];

/** Repeats a short list until it can fill a wide screen, then doubles it so the loop has no seam. */
function tapeItems(items: string[]) {
  const base = items.length > 0 ? items : fallbackTape;
  const filled: string[] = [];
  while (filled.length < 8) filled.push(...base);
  return [...filled, ...filled];
}

function Tape({ items, className }: { items: string[]; className: string }) {
  return (
    <div className={`tape ${className}`}>
      <div className="tape-track">
        {tapeItems(items).map((item, index) => (
          <span className="tape-item" key={index}>
            {item}
            <BrandMark className="tape-mark" />
          </span>
        ))}
      </div>
    </div>
  );
}

/** Top-down pitch markings (105 × 68 m at 10 units per metre), laid flat in perspective by the CSS. */
function Pitch() {
  return (
    <div className="pitch" aria-hidden="true">
      <svg viewBox="0 0 1050 680" preserveAspectRatio="xMidYMid meet">
        <rect pathLength={1} x="5" y="5" width="1040" height="670" />
        <line pathLength={1} x1="525" y1="5" x2="525" y2="675" />
        <circle pathLength={1} cx="525" cy="340" r="91.5" />
        <circle className="pitch-spot" cx="525" cy="340" r="5" />
        <rect pathLength={1} x="5" y="138" width="165" height="404" />
        <rect pathLength={1} x="880" y="138" width="165" height="404" />
        <rect pathLength={1} x="5" y="248" width="55" height="184" />
        <rect pathLength={1} x="990" y="248" width="55" height="184" />
        <path pathLength={1} d="M170 267a91.5 91.5 0 0 1 0 146" />
        <path pathLength={1} d="M880 267a91.5 91.5 0 0 0 0 146" />
      </svg>
    </div>
  );
}

function tapeNames(competitions: Competition[]) {
  return competitions.filter((competition) => competition.status !== "completed").map((competition) => competition.name);
}

function tapeFormats(competitions: Competition[]) {
  const formats = [...new Set(competitions.map((competition) => formatLabel(competition.format)))];
  return ["eFootball Mobile", ...formats];
}

export default async function Home() {
  const { data } = await loadCompetitions();
  const open = data.filter((competition) => openStatuses.has(competition.status));

  return (
    <>
      <SiteHeader />

      <main>
        <section className="hero" aria-labelledby="hero-title">
          <div className="hero-bg" aria-hidden="true">
            <div className="aurora aurora-a" />
            <div className="aurora aurora-b" />
            <div className="hero-dots hero-dots-a" />
            <div className="hero-dots hero-dots-b" />
            <Pitch />
          </div>

          <div className="hero-stage" aria-hidden="true">
            <div className="spot spot-left" />
            <div className="spot spot-right" />
            <div className="player player-left">
              {/* eslint-disable-next-line @next/next/no-img-element -- static art, already sized and compressed */}
              <img src="/images/player-left.webp" alt="" width={1254} height={1197} fetchPriority="high" draggable={false} />
            </div>
            <div className="player player-right">
              {/* eslint-disable-next-line @next/next/no-img-element -- static art, already sized and compressed */}
              <img src="/images/player-right.webp" alt="" width={1277} height={1196} fetchPriority="high" draggable={false} />
            </div>
            <div className="clash">
              <i />
              <i />
            </div>
          </div>

          <div className="hero-copy shell">
            <h1 id="hero-title">
              <span className="line">
                <span>Your game.</span>
              </span>
              <span className="line">
                <span className="shine">Your name.</span>
              </span>
              <span className="swash" aria-hidden="true" />
            </h1>
            <div className="hero-actions">
              <Link className="button button-primary" href="/tournaments">
                Browse tournaments <ArrowIcon className="button-icon" />
              </Link>
              <Link className="button button-glass" href="#app">
                Get the app
              </Link>
            </div>
          </div>
        </section>

        <div className="tapes" aria-hidden="true">
          <Tape className="tape-back" items={tapeFormats(data)} />
          <Tape className="tape-front" items={tapeNames(data)} />
        </div>

        {open.length > 0 && (
          <section className="section shell" aria-labelledby="open-now">
            <div className="section-head" data-reveal>
              <h2 id="open-now">Open now</h2>
              <Link className="text-link" href="/tournaments">
                All tournaments <ArrowIcon className="inline-icon" />
              </Link>
            </div>
            <div className="competition-grid competition-grid-preview">
              {open.slice(0, 3).map((competition, index) => (
                <CompetitionCard competition={competition} index={index} key={competition.id} />
              ))}
            </div>
          </section>
        )}

        <section className="section shell" id="how-it-works" aria-labelledby="how-it-works-title">
          <div className="section-head" data-reveal>
            <h2 id="how-it-works-title">How it works</h2>
          </div>
          <ol className="steps">
            {steps.map(({ title, body, Icon }, index) => (
              <li className="card step" data-reveal style={{ "--i": index } as CSSProperties} key={title}>
                <span className="step-index" aria-hidden="true">
                  {String(index + 1).padStart(2, "0")}
                </span>
                <span className="step-icon">
                  <Icon />
                </span>
                <h3>{title}</h3>
                <p>{body}</p>
              </li>
            ))}
          </ol>
        </section>

        <section className="section shell" id="app" aria-labelledby="app-title">
          <div className="app-band" data-reveal>
            {/* The kit itself: black fabric, the volt sleeve print, the blaze
                shoulder and the cobalt collar, all lifted from the player art. */}
            <div className="jersey" aria-hidden="true">
              <span className="jersey-collar" />
              <span className="jersey-patch" />
              <span className="jersey-streak" />
            </div>
            <div className="app-band-copy">
              <BrandMark className="crest" />
              <h2 id="app-title">
                Your matches,
                <br />
                in your pocket.
              </h2>
              <div className="store-row">
                <span className="store-badge">
                  <AndroidIcon />
                  <span>
                    <small>Coming soon</small>
                    Android
                  </span>
                </span>
                <span className="store-badge">
                  <PhoneIcon />
                  <span>
                    <small>Coming soon</small>
                    iPhone
                  </span>
                </span>
              </div>
            </div>
          </div>
        </section>
      </main>

      <SiteFooter />
    </>
  );
}
