import Link from "next/link";
import type { CSSProperties } from "react";
import { ArrowIcon } from "./icons";
import {
  type Competition,
  countdownParts,
  dateParts,
  entryLabel,
  fillPercent,
  formatLabel,
  statusLabel,
} from "./lib/competitions";

const statusTone: Record<string, string> = {
  registration_open: "open",
  check_in: "open",
  running: "live",
};

function capacity(competition: Competition) {
  const fill = fillPercent(competition);
  const open = statusTone[competition.status] === "open";
  return {
    fill,
    hot: open && fill >= 70,
    full: open && competition.availableSlots <= 0,
  };
}

function Status({ competition }: { competition: Competition }) {
  return (
    <span className={`status status-${statusTone[competition.status] ?? "quiet"}`}>
      <i aria-hidden="true" />
      {statusLabel(competition.status)}
    </span>
  );
}

export function TournamentRow({ competition, index }: { competition: Competition; index: number }) {
  const date = dateParts(competition.startsAt);
  const { fill, hot, full } = capacity(competition);
  const style = { "--i": index % 8, "--fill": fill / 100 } as CSSProperties;

  return (
    <li className="board-row tournament-row" data-reveal data-hot={hot || full || undefined} style={style}>
      <div className="date-block" aria-hidden={date ? undefined : true}>
        {date ? (
          <>
            <span>{date.month}</span>
            <strong>{date.day}</strong>
            <small>{date.weekday}</small>
          </>
        ) : (
          <strong>–</strong>
        )}
      </div>

      <div className="row-main">
        <h3>{competition.name}</h3>
        <p className="row-meta">
          <span>{formatLabel(competition.format)}</span>
          {date && (
            <time dateTime={date.iso} data-local-time="time">
              {date.time}
            </time>
          )}
        </p>
      </div>

      <div className="row-cell row-fee">
        <span className="cell-label">Entry</span>
        <strong className={competition.entryType === "paid" ? undefined : "is-free"}>{entryLabel(competition)}</strong>
      </div>

      <div className="row-cell row-players">
        <span className="cell-label">Players</span>
        <strong>
          {competition.entryCount}
          <small> / {competition.maxEntries}</small>
        </strong>
        <div className="capacity-bar">
          <span />
        </div>
      </div>

      <div className="row-cell row-status">
        <Status competition={competition} />
        {full && <span className="tag-hot">Full</span>}
        {hot && !full && <span className="tag-hot">Almost full</span>}
      </div>
    </li>
  );
}

const units = [
  ["days", "Days"],
  ["hours", "Hrs"],
  ["minutes", "Min"],
  ["seconds", "Sec"],
] as const;

/** The next tournament to start, presented as the event of the page. */
export function FeaturedTournament({ competition }: { competition: Competition }) {
  const date = dateParts(competition.startsAt);
  const { fill, hot, full } = capacity(competition);
  const left = countdownParts(competition.startsAt);
  const style = { "--fill": fill / 100 } as CSSProperties;

  return (
    <article className="featured" data-reveal data-hot={hot || full || undefined} style={style} aria-labelledby="featured-title">
      <div className="featured-art" aria-hidden="true">
        <span className="featured-spot" />
        <span className="featured-streak" />
        {/* eslint-disable-next-line @next/next/no-img-element -- static art, already sized and compressed */}
        <img className="featured-player" src="/images/player-right.webp" alt="" width={1277} height={1196} />
      </div>

      <div className="featured-body">
        <Status competition={competition} />
        <h2 id="featured-title">{competition.name}</h2>
        <ul className="featured-meta">
          <li>{formatLabel(competition.format)}</li>
          {date && (
            <li>
              <time dateTime={date.iso} data-local-time="datetime">
                {date.weekday} {date.day} {date.month} · {date.time}
              </time>
            </li>
          )}
          <li>{competition.entryType === "paid" ? `${entryLabel(competition)} entry` : "Free entry"}</li>
        </ul>

        <div className="featured-stats">
          {date && (
            <div className="countdown" data-countdown={date.iso} role="timer" aria-label="Time until it starts">
              {units.map(([unit, label]) => (
                <span className="countdown-unit" key={unit}>
                  <strong data-unit={unit}>{String(left[unit]).padStart(2, "0")}</strong>
                  <small>{label}</small>
                </span>
              ))}
            </div>
          )}
          <div className="featured-capacity">
            <div className="capacity-row">
              <span>Players</span>
              <span>
                <strong>{competition.entryCount}</strong> / {competition.maxEntries}
              </span>
            </div>
            <div className="capacity-bar">
              <span />
            </div>
          </div>
        </div>

        <Link className="button button-primary" href="/#app">
          Join in the app <ArrowIcon className="button-icon" />
        </Link>
      </div>
    </article>
  );
}
