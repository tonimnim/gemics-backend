import type { Metadata } from "next";
import Link from "next/link";
import type { CSSProperties } from "react";
import { PageHead } from "../page-head";
import {
  type RankedPlayer,
  avatarSource,
  avatarTone,
  initials,
  loadRankings,
  rankingCountries,
  rankingCountry,
  updatedLabel,
} from "../lib/rankings";
import { SiteFooter, SiteHeader } from "../site-chrome";

export const metadata: Metadata = {
  title: "Rankings — Tonits",
};

function Avatar({ player, size = "md" }: { player: RankedPlayer; size?: "md" | "lg" }) {
  const source = avatarSource(player);
  return (
    <span className={`avatar avatar-${size} avatar-tone-${avatarTone(player.playerId)}`} aria-hidden="true">
      {source ? (
        // eslint-disable-next-line @next/next/no-img-element -- served by the API, sized by CSS
        <img src={source} alt="" loading="lazy" />
      ) : (
        initials(player)
      )}
    </span>
  );
}

function Movement({ value }: { value: number }) {
  if (value > 0) {
    return (
      <span className="move move-up" title={`Up ${value}`}>
        ▲ {value}
      </span>
    );
  }
  if (value < 0) {
    return (
      <span className="move move-down" title={`Down ${-value}`}>
        ▼ {-value}
      </span>
    );
  }
  return (
    <span className="move" title="No change">
      –
    </span>
  );
}

const podiumPlace = ["first", "second", "third"];

function Podium({ players }: { players: RankedPlayer[] }) {
  return (
    <ol className="podium" aria-label="Top three">
      {players.map((player, index) => (
        <li
          className={`card podium-spot podium-${podiumPlace[index]}`}
          data-reveal
          style={{ "--i": index } as CSSProperties}
          key={player.playerId}
        >
          <span className="podium-rank" aria-hidden="true">
            {player.rank}
          </span>
          <Avatar player={player} size="lg" />
          <h3>{player.displayName}</h3>
          <p className="podium-handle">@{player.handle}</p>
          <dl className="podium-stats">
            <div>
              <dt>Rating</dt>
              <dd>{player.rating}</dd>
            </div>
            <div>
              <dt>Played</dt>
              <dd>{player.matchesPlayed}</dd>
            </div>
          </dl>
        </li>
      ))}
    </ol>
  );
}

export default async function Rankings({
  searchParams,
}: {
  searchParams: Promise<Record<string, string | string[] | undefined>>;
}) {
  const params = await searchParams;
  const country = rankingCountry(typeof params.country === "string" ? params.country : undefined);
  const cursor = typeof params.cursor === "string" && params.cursor.length < 2048 ? params.cursor : null;
  const result = await loadRankings(country?.code ?? null, cursor);
  const scopeHref = country ? `/rankings?country=${country.code}` : "/rankings";

  const data = result.state === "ok" ? result.data : [];
  const firstPage = !cursor;
  const podium = firstPage ? data.slice(0, 3) : [];
  const rows = firstPage ? data.slice(3) : data;
  const updated = result.state === "ok" ? updatedLabel(result.snapshotAt) : null;

  return (
    <>
      <SiteHeader />

      <main>
        <PageHead title="Rankings" meta={updated}>
          <nav className="filters" aria-label="Ranking region">
            <Link className="filter" href="/rankings" aria-current={country ? undefined : "page"} scroll={false}>
              Global
            </Link>
            {rankingCountries.map((option) => (
              <Link
                className="filter"
                href={`/rankings?country=${option.code}`}
                aria-current={country?.code === option.code ? "page" : undefined}
                key={option.code}
                scroll={false}
              >
                {option.name}
              </Link>
            ))}
          </nav>
        </PageHead>

        <div className="shell">
          {result.state === "unavailable" && <p className="empty-note">Rankings are unavailable right now. Try again shortly.</p>}

          {result.state === "expired" && (
            <p className="empty-note">
              The board has been updated since this page loaded. <Link href={scopeHref}>Start from the top</Link>
            </p>
          )}

          {result.state === "ok" && data.length === 0 && (
            <div className="empty-board">
              <span className="empty-board-mark" aria-hidden="true" />
              <p>The board fills up as matches finish.</p>
            </div>
          )}

          {podium.length > 0 && <Podium players={podium} />}

          {rows.length > 0 && (
            <div className="board" role="table" aria-label="Rankings">
              <div className="board-head ranking-row" role="row">
                <span role="columnheader">Rank</span>
                <span role="columnheader" className="col-move">
                  +/-
                </span>
                <span role="columnheader">Player</span>
                <span role="columnheader" className="col-country">
                  Region
                </span>
                <span role="columnheader" className="col-played">
                  Played
                </span>
                <span role="columnheader" className="col-rating">
                  Rating
                </span>
              </div>
              {rows.map((player, index) => (
                <div
                  className="board-row ranking-row"
                  role="row"
                  data-reveal
                  style={{ "--i": index % 8 } as CSSProperties}
                  key={player.playerId}
                >
                  <span role="cell" className="col-rank">
                    {player.rank}
                  </span>
                  <span role="cell" className="col-move">
                    <Movement value={player.rankMovement} />
                  </span>
                  <span role="cell" className="col-player">
                    <Avatar player={player} />
                    <span className="player-names">
                      <strong>{player.displayName}</strong>
                      <small>@{player.handle}</small>
                    </span>
                  </span>
                  <span role="cell" className="col-country">
                    <span className="country-code">{player.countryCode}</span>
                  </span>
                  <span role="cell" className="col-played">
                    {player.matchesPlayed}
                  </span>
                  <span role="cell" className="col-rating">
                    {player.rating}
                  </span>
                </div>
              ))}
            </div>
          )}

          {result.state === "ok" && (result.nextCursor || !firstPage) && (
            <div className="board-pager">
              {!firstPage && (
                <Link className="button button-glass button-small" href={scopeHref}>
                  Back to top
                </Link>
              )}
              {result.nextCursor && (
                <Link
                  className="button button-glass button-small"
                  href={`${scopeHref}${country ? "&" : "?"}cursor=${encodeURIComponent(result.nextCursor)}`}
                >
                  Next 50
                </Link>
              )}
            </div>
          )}
        </div>
      </main>

      <SiteFooter />
    </>
  );
}
