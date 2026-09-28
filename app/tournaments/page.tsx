import Link from "next/link";
import { BrandMark } from "../brand-mark";

// Rendered on the server, so the page ships no client JavaScript and needs no
// CORS grant. Inside Docker the API is reachable as the compose service name;
// the localhost fallback is for `npm run dev` on a developer machine.
const apiURL = process.env.API_URL ?? "http://localhost:8080";

type Competition = {
  id: string;
  slug: string;
  name: string;
  description: string;
  gameName: string;
  organizerName: string;
  format: string;
  status: string;
  maxEntries: number;
  entryCount: number;
  availableSlots: number;
  entryType: "free" | "paid";
  entryFeeMinor: number;
  currency: string;
  startsAt: string;
};

const formatLabels: Record<string, string> = {
  single_elimination: "Single elimination",
  double_elimination: "Double elimination",
  round_robin: "Round robin",
};

const statusLabels: Record<string, string> = {
  published: "Announced",
  registration_open: "Registration open",
  check_in: "Check-in open",
  running: "Underway",
  completed: "Finished",
};

/** Entry fees are stored as integer minor units, never floats. */
function entryPrice(competition: Competition) {
  const amount = competition.entryFeeMinor / 100;
  return new Intl.NumberFormat("en-KE", {
    style: "currency",
    currency: competition.currency || "KES",
    minimumFractionDigits: Number.isInteger(amount) ? 0 : 2,
  }).format(amount);
}

function startLabel(value: string) {
  const startsAt = new Date(value);
  if (Number.isNaN(startsAt.getTime())) return "Date to be confirmed";
  return startsAt.toLocaleDateString("en-KE", { day: "numeric", month: "short", year: "numeric" });
}

async function loadCompetitions(): Promise<{ data: Competition[]; reachable: boolean }> {
  try {
    // no-store because a tournament that just opened should appear immediately;
    // the API already fronts this query with its own short-lived cache.
    const response = await fetch(`${apiURL}/v1/competitions?limit=50`, { cache: "no-store" });
    if (!response.ok) return { data: [], reachable: false };
    const body = (await response.json()) as { data?: Competition[] };
    return { data: body.data ?? [], reachable: true };
  } catch {
    return { data: [], reachable: false };
  }
}

export default async function Tournaments() {
  const { data, reachable } = await loadCompetitions();
  const accents = ["", "blue", "orange"];

  return (
    <main>
      <header className="site-header shell">
        <Link className="brand" href="/" aria-label="Gamics home">
          <BrandMark />
          <span>GAMICS</span>
        </Link>
        <nav aria-label="Primary navigation">
          <Link href="/tournaments">Tournaments</Link>
          <Link href="/#players">Players</Link>
          <Link href="/#organizers">Organizers</Link>
        </nav>
        <button className="button button-quiet" type="button">Join the arena</button>
      </header>

      <section className="section shell">
        <div className="section-heading">
          <div>
            <p className="eyebrow"><span /> All competitions</p>
            <h2>FIND YOUR<br />TOURNAMENT.</h2>
          </div>
          <p>Every open eFootball Mobile competition on Gamics. Free to enter unless a fee is shown.</p>
        </div>

        {!reachable && (
          <p className="tournament-note">
            Competitions are temporarily unavailable. Please try again shortly.
          </p>
        )}

        {reachable && data.length === 0 && (
          <p className="tournament-note">
            No competitions are open right now. Check back soon.
          </p>
        )}

        {data.length > 0 && (
          <div className="competition-grid">
            {data.map((competition, index) => (
              <article className="competition-card" key={competition.id}>
                <div className={`competition-number ${accents[index % accents.length]}`}>
                  {String(index + 1).padStart(2, "0")}
                </div>
                <div className="competition-content">
                  <span className="game-label">{competition.gameName}</span>
                  <h3>{competition.name}</h3>
                  {competition.description && <p>{competition.description}</p>}

                  <div className="entry-row">
                    <span className={`entry-tag ${competition.entryType}`}>
                      {competition.entryType === "paid" ? entryPrice(competition) : "FREE ENTRY"}
                    </span>
                    <span className="entry-format">{formatLabels[competition.format] ?? competition.format}</span>
                  </div>

                  <div className="competition-meta">
                    <span>{statusLabels[competition.status] ?? competition.status} &middot; {startLabel(competition.startsAt)}</span>
                    <strong>{competition.entryCount} / {competition.maxEntries}</strong>
                  </div>
                  <div className="capacity">
                    <i style={{ width: `${Math.min(100, Math.round((competition.entryCount / Math.max(1, competition.maxEntries)) * 100))}%` }} />
                  </div>
                  <p className="entry-organizer">By {competition.organizerName}</p>
                </div>
              </article>
            ))}
          </div>
        )}
      </section>

      <footer className="shell">
        <div className="footer-row">
          <Link className="brand" href="/"><BrandMark /><span>GAMICS</span></Link>
          <p>Competition infrastructure for African esports.</p>
          <span>NAIROBI // 2026</span>
        </div>
        <p className="legal-note">
          eFootball is a trademark of Konami Digital Entertainment. Gamics is an independent platform and is not
          affiliated with or endorsed by Konami.
        </p>
      </footer>
    </main>
  );
}
