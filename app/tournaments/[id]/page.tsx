import type { Metadata } from "next";
import Link from "next/link";
import { notFound } from "next/navigation";
import type { CSSProperties } from "react";
import { Bracket } from "../../bracket";
import { ArrowIcon } from "../../icons";
import {
  type CompetitionDetail,
  type Standings,
  dateParts,
  entryLabel,
  fillPercent,
  formatLabel,
  loadBracket,
  loadCompetition,
  loadStandings,
  matchFormatLabel,
  moneyLabel,
  statusLabel,
} from "../../lib/competitions";
import { PageHead } from "../../page-head";
import { SiteFooter, SiteHeader } from "../../site-chrome";

type Props = { params: Promise<{ id: string }> };

export async function generateMetadata({ params }: Props): Promise<Metadata> {
  const { id } = await params;
  const result = await loadCompetition(id);
  return { title: result.state === "ok" ? `${result.data.name} — Ozone` : "Tournament — Ozone" };
}

const joinable = new Set(["registration_open", "check_in"]);

function Fact({ label, children }: { label: string; children: React.ReactNode }) {
  return (
    <div className="fact">
      <span className="cell-label">{label}</span>
      <strong>{children}</strong>
    </div>
  );
}

function When({ value, fallback }: { value: string | null; fallback: string }) {
  const date = value ? dateParts(value) : null;
  if (!date) return <>{fallback}</>;
  return (
    <time dateTime={date.iso} data-local-time="datetime">
      {date.weekday} {date.day} {date.month} · {date.time}
    </time>
  );
}

function Facts({ competition }: { competition: CompetitionDetail }) {
  const style = { "--fill": fillPercent(competition) / 100 } as CSSProperties;
  return (
    <div className="facts" style={style}>
      <Fact label="Starts">
        <When value={competition.startsAt} fallback="To be confirmed" />
      </Fact>
      <Fact label="Entry">{entryLabel(competition)}</Fact>
      <Fact label="Prize">
        {competition.prizeAmountMinor > 0 ? moneyLabel(competition.prizeAmountMinor, competition.currency) : "None"}
      </Fact>
      <Fact label="Players">
        {competition.entryCount}
        <small> / {competition.maxEntries}</small>
        <span className="capacity-bar">
          <span />
        </span>
      </Fact>
      <Fact label="Format">{formatLabel(competition.format)}</Fact>
      <Fact label="Games per match">{matchFormatLabel(competition.matchFormat)}</Fact>
      {joinable.has(competition.status) && (
        <Fact label="Registration closes">
          <When value={competition.registrationClosesAt} fallback="To be confirmed" />
        </Fact>
      )}
    </div>
  );
}

function StandingsTables({ standings, series }: { standings: Standings; series: boolean }) {
  const groups = standings.stages.flatMap((stage) => stage.groups);
  return (
    <>
      {groups.map((group) => (
        <div className="board standings" role="table" aria-label={group.key === "main" ? "Table" : `Group ${group.key}`} key={group.key}>
          {groups.length > 1 && <p className="standings-title">Group {group.key.replace(/^group_/, "").toUpperCase()}</p>}
          <div className="board-head standings-row" role="row">
            <span role="columnheader">#</span>
            <span role="columnheader">Player</span>
            <span role="columnheader">P</span>
            <span role="columnheader">W</span>
            <span role="columnheader">D</span>
            <span role="columnheader">L</span>
            <span role="columnheader">{series ? "Games" : "GD"}</span>
            <span role="columnheader">Pts</span>
          </div>
          {group.rows.map((row) => (
            <div className={`board-row standings-row${row.removed ? " is-out" : ""}`} role="row" key={row.entryId}>
              <span role="cell">{row.rank ?? "–"}</span>
              <span role="cell" className="standings-name">
                {row.displayName}
              </span>
              <span role="cell">{row.played}</span>
              <span role="cell">{row.wins}</span>
              <span role="cell">{row.draws}</span>
              <span role="cell">{row.losses}</span>
              <span role="cell">
                {series ? `${row.gamesWon}-${row.gamesLost}` : row.goalDifference > 0 ? `+${row.goalDifference}` : row.goalDifference}
              </span>
              <span role="cell" className="standings-points">
                {row.points}
              </span>
            </div>
          ))}
        </div>
      ))}
    </>
  );
}

const placeNames = ["Champion", "Runner-up", "Third"];

export default async function Tournament({ params }: Props) {
  const { id } = await params;
  const result = await loadCompetition(id);
  if (result.state === "missing") notFound();

  if (result.state === "unavailable") {
    return (
      <>
        <SiteHeader />
        <main>
          <PageHead title="Tournament" />
          <div className="shell">
            <p className="empty-note">This tournament is unavailable right now. Try again shortly.</p>
          </div>
        </main>
        <SiteFooter />
      </>
    );
  }

  const competition = result.data;
  const drawn = !["published", "registration_open", "check_in"].includes(competition.status);
  const [stages, standings] = drawn ? await Promise.all([loadBracket(id), loadStandings(id)]) : [null, null];
  const bracketStages = (stages ?? []).filter((stage) => stage.rounds.length > 0);
  const roundRobin = competition.format === "round_robin";
  const series = (competition.matchFormat?.bestOf ?? 1) > 1;
  const podium = (standings?.placements ?? []).filter((place) => place.placement <= 3);

  return (
    <>
      <SiteHeader />

      <main>
        <PageHead title={competition.name} meta={`${statusLabel(competition.status)} · ${competition.organizerName}`}>
          <Facts competition={competition} />
          <div className="detail-actions">
            {joinable.has(competition.status) ? (
              <Link className="button button-primary" href="/#app">
                Join in the app <ArrowIcon className="button-icon" />
              </Link>
            ) : (
              <Link className="button button-glass" href="/#app">
                Follow it in the app
              </Link>
            )}
            <Link className="button button-glass" href="/tournaments">
              All tournaments
            </Link>
          </div>
        </PageHead>

        <div className="shell detail-body">
          {competition.description && <p className="detail-description">{competition.description}</p>}

          {podium.length > 0 && (
            <section aria-labelledby="placements-title">
              <h2 className="section-title" id="placements-title">
                Final placings
              </h2>
              <ol className="placings">
                {podium.map((place) => (
                  <li className={`placing placing-${place.placement}`} key={place.entryId}>
                    <span className="placing-rank">{placeNames[place.placement - 1]}</span>
                    <strong>{place.displayName}</strong>
                  </li>
                ))}
              </ol>
            </section>
          )}

          {roundRobin && standings && standings.stages.length > 0 && (
            <section aria-labelledby="table-title">
              <h2 className="section-title" id="table-title">
                Table
              </h2>
              <StandingsTables standings={standings} series={series} />
            </section>
          )}

          {bracketStages.length > 0 ? (
            <section aria-labelledby="bracket-title">
              <h2 className="section-title" id="bracket-title">
                {roundRobin ? "Fixtures" : "Bracket"}
              </h2>
              {bracketStages.map((stage) => (
                <Bracket stage={stage} key={stage.id} />
              ))}
            </section>
          ) : (
            <p className="empty-note">
              {drawn ? "The bracket is unavailable right now." : "The bracket is drawn when registration closes."}
            </p>
          )}
        </div>
      </main>

      <SiteFooter />
    </>
  );
}
