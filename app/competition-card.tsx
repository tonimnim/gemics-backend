import type { CSSProperties } from "react";
import { type Competition, entryLabel, fillPercent, formatLabel, startLabel, statusLabel } from "./lib/competitions";

const statusTone: Record<string, string> = {
  registration_open: "open",
  check_in: "open",
  running: "live",
};

export function CompetitionCard({ competition, index = 0 }: { competition: Competition; index?: number }) {
  const fill = fillPercent(competition);
  const tone = statusTone[competition.status] ?? "quiet";
  const almostFull = tone === "open" && competition.availableSlots > 0 && fill >= 70;
  const full = tone === "open" && competition.availableSlots <= 0;
  const style = { "--i": index % 3, "--fill": fill / 100 } as CSSProperties;

  return (
    <article className="card competition-card" data-reveal data-hot={almostFull || full || undefined} style={style}>
      <div className="card-top">
        <span className={competition.entryType === "paid" ? "fee" : "fee fee-free"}>{entryLabel(competition)}</span>
        <span className={`status status-${tone}`}>
          <i aria-hidden="true" />
          {statusLabel(competition.status)}
        </span>
      </div>

      <h3>{competition.name}</h3>

      <ul className="card-tags">
        <li>{formatLabel(competition.format)}</li>
        <li>{startLabel(competition.startsAt)}</li>
        {full && <li className="tag-hot">Full</li>}
        {almostFull && <li className="tag-hot">Almost full</li>}
      </ul>

      <div className="capacity">
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
    </article>
  );
}
