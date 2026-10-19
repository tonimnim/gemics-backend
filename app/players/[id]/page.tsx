import type { Metadata } from "next";
import Link from "next/link";
import { notFound } from "next/navigation";
import { formatLabel } from "../../lib/competitions";
import { type PlayerMatch, type PublicPlayer, loadPlayer, loadPlayerCompetitions, loadPlayerMatches } from "../../lib/players";
import { avatarSource, avatarTone, initials, rankingCountry } from "../../lib/rankings";
import { PageHead } from "../../page-head";
import { SiteFooter, SiteHeader } from "../../site-chrome";

type Props = { params: Promise<{ id: string }> };

export async function generateMetadata({ params }: Props): Promise<Metadata> {
  const { id } = await params;
  const result = await loadPlayer(id);
  return { title: result.state === "ok" ? `${result.data.displayName} — Ozone` : "Player — Ozone" };
}

function ordinal(value: number) {
  const tens = value % 100;
  if (tens >= 11 && tens <= 13) return `${value}th`;
  return `${value}${["th", "st", "nd", "rd"][value % 10] ?? "th"}`;
}

function shortDate(value: string) {
  const at = new Date(value);
  return Number.isNaN(at.getTime()) ? "" : at.toLocaleDateString("en-GB", { day: "numeric", month: "short", timeZone: "UTC" });
}

const outcomeLabels: Record<string, string> = { win: "W", loss: "L", draw: "D" };

function score(match: PlayerMatch) {
  const pens = match.score.tiebreak ? ` (pens)` : "";
  return `${match.score.player}–${match.score.opponent}${pens}`;
}

function Header({ player }: { player: PublicPlayer }) {
  const rating = player.ratings[0];
  const source = avatarSource(player);
  const country = rankingCountry(player.countryCode);
  return (
    <div className="player-card">
      <span className={`avatar avatar-xl avatar-tone-${avatarTone(player.playerId)}`} aria-hidden="true">
        {source ? (
          // eslint-disable-next-line @next/next/no-img-element -- served by the API, sized by CSS
          <img src={source} alt="" />
        ) : (
          initials(player)
        )}
      </span>
      <div className="player-card-names">
        <p className="player-handle">@{player.handle}</p>
        <p className="player-region">
          {country?.name ?? player.countryCode} · Joined {new Date(player.joinedAt).toLocaleDateString("en-GB", { month: "short", year: "numeric" })}
        </p>
        {player.bio && <p className="player-bio">{player.bio}</p>}
      </div>
      <dl className="player-stats">
        <div>
          <dt>Rating</dt>
          <dd>{rating ? rating.rating : "–"}</dd>
        </div>
        <div>
          <dt>{country ? `${country.name} rank` : "Country rank"}</dt>
          <dd>{rating?.countryRank ? `#${rating.countryRank}` : "–"}</dd>
        </div>
        <div>
          <dt>Global rank</dt>
          <dd>{rating?.globalRank ? `#${rating.globalRank}` : "–"}</dd>
        </div>
        <div>
          <dt>Record</dt>
          <dd>
            {player.record.wins}-{player.record.draws}-{player.record.losses}
          </dd>
        </div>
      </dl>
    </div>
  );
}

export default async function Player({ params }: Props) {
  const { id } = await params;
  const result = await loadPlayer(id);
  // A private profile reads as missing, exactly as the API answers it.
  if (result.state === "missing") notFound();

  if (result.state === "unavailable") {
    return (
      <>
        <SiteHeader />
        <main>
          <PageHead title="Player" />
          <div className="shell">
            <p className="empty-note">This player is unavailable right now. Try again shortly.</p>
          </div>
        </main>
        <SiteFooter />
      </>
    );
  }

  const player = result.data;
  const [matches, competitions] = await Promise.all([loadPlayerMatches(id), loadPlayerCompetitions(id)]);

  return (
    <>
      <SiteHeader />

      <main>
        <PageHead title={player.displayName}>
          <Header player={player} />
        </PageHead>

        <div className="shell detail-body">
          <section aria-labelledby="matches-title">
            <h2 className="section-title" id="matches-title">
              Recent matches
            </h2>
            {matches.length === 0 ? (
              <p className="empty-note">No matches played yet.</p>
            ) : (
              <ol className="board history">
                {matches.map((match) => (
                  <li className="board-row history-row" key={match.matchId}>
                    <span className={`outcome outcome-${match.outcome}`}>{outcomeLabels[match.outcome] ?? "–"}</span>
                    <span className="history-main">
                      <strong>
                        {match.opponent ? (
                          <Link href={`/players/${match.opponent.playerId}`}>{match.opponent.displayName}</Link>
                        ) : (
                          "Walkover"
                        )}
                      </strong>
                      <small>
                        <Link href={`/tournaments/${match.competition.competitionId}`}>{match.competition.name}</Link> · {shortDate(match.playedAt)}
                      </small>
                    </span>
                    <span className="history-score">{match.opponent ? score(match) : "–"}</span>
                  </li>
                ))}
              </ol>
            )}
          </section>

          <section aria-labelledby="competitions-title">
            <h2 className="section-title" id="competitions-title">
              Tournaments
            </h2>
            {competitions.length === 0 ? (
              <p className="empty-note">No tournaments entered yet.</p>
            ) : (
              <ol className="board history">
                {competitions.map((competition) => (
                  <li className="board-row history-row" key={competition.competitionId}>
                    <span className="placing-badge">{competition.placement ? ordinal(competition.placement) : "–"}</span>
                    <span className="history-main">
                      <strong>
                        <Link href={`/tournaments/${competition.competitionId}`}>{competition.name}</Link>
                      </strong>
                      <small>
                        {formatLabel(competition.format)} · {competition.entrants} players · {shortDate(competition.startedAt)}
                      </small>
                    </span>
                    <span className="history-score">
                      {competition.record.wins}-{competition.record.draws}-{competition.record.losses}
                    </span>
                  </li>
                ))}
              </ol>
            )}
          </section>
        </div>
      </main>

      <SiteFooter />
    </>
  );
}
