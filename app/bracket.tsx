import type { CSSProperties } from "react";
import type { BracketMatch, BracketRound, BracketStage } from "./lib/competitions";

// The order brackets are shown in, and their headings when a stage has more
// than one. A single-elimination stage has only "main".
const bracketOrder = ["main", "winners", "losers", "grand_final", "bronze"];
const bracketTitles: Record<string, string> = {
  winners: "Winners bracket",
  losers: "Losers bracket",
  grand_final: "Grand final",
  bronze: "Third place",
};

/** Knockout rounds named back from the final, as players say them. */
function roundTitle(round: BracketRound, lastMainRound: number) {
  if (round.bracket === "bronze") return "Third place";
  if (round.bracket !== "main") return round.name;
  const fromFinal = lastMainRound - round.number;
  if (fromFinal === 0) return "Final";
  if (fromFinal === 1) return "Semifinal";
  if (fromFinal === 2) return "Quarterfinal";
  return `Round of ${2 ** (fromFinal + 1)}`;
}

function Side({ match, side }: { match: BracketMatch; side: "home" | "away" }) {
  const entry = match[side].participant;
  const score = match.score ? (side === "home" ? match.score.homeScore : match.score.awayScore) : null;
  const penalties = match.score?.tiebreak ? (side === "home" ? match.score.tiebreak.homeScore : match.score.tiebreak.awayScore) : null;
  const won = !!entry && match.winnerEntryId === entry.entryId;
  const lost = !!entry && !!match.winnerEntryId && !won;
  return (
    <div className={`bracket-side${won ? " is-winner" : ""}${lost ? " is-out" : ""}`}>
      <span className="bracket-name">
        {entry?.seed != null && <small className="bracket-seed">{entry.seed}</small>}
        {entry ? entry.displayName : <em>To be decided</em>}
      </span>
      {score !== null && (
        <span className="bracket-score">
          {score}
          {penalties !== null && <small> ({penalties})</small>}
        </span>
      )}
    </div>
  );
}

const liveStates = new Set(["in_progress", "awaiting_confirmation", "disputed"]);

function MatchCard({ match }: { match: BracketMatch }) {
  return (
    <li className={`bracket-match${liveStates.has(match.state) ? " is-live" : ""}`}>
      <Side match={match} side="home" />
      <Side match={match} side="away" />
      {liveStates.has(match.state) && <span className="bracket-live">Live</span>}
    </li>
  );
}

/** A stage's matches as columns, one per round, earliest on the left. */
export function Bracket({ stage }: { stage: BracketStage }) {
  const brackets = [...new Set(stage.rounds.map((round) => round.bracket))].sort(
    (a, b) => (bracketOrder.indexOf(a) + 1 || 99) - (bracketOrder.indexOf(b) + 1 || 99),
  );
  const lastMainRound = Math.max(0, ...stage.rounds.filter((round) => round.bracket === "main").map((round) => round.number));

  return (
    <>
      {brackets.map((key) => {
        const rounds = stage.rounds.filter((round) => round.bracket === key);
        return (
          <section className="bracket-group" key={key} aria-label={bracketTitles[key] ?? "Bracket"}>
            {brackets.length > 1 && bracketTitles[key] && <h3 className="bracket-group-title">{bracketTitles[key]}</h3>}
            <div className="bracket" style={{ "--rounds": rounds.length } as CSSProperties}>
              {rounds.map((round) => {
                const bestOf = round.matches[0]?.bestOf ?? 1;
                return (
                  <div className="bracket-round" key={`${round.bracket}-${round.number}`}>
                    <h4>
                      {roundTitle(round, lastMainRound)}
                      {bestOf > 1 && <span className="bracket-bo">Best of {bestOf}</span>}
                    </h4>
                    <ol>
                      {round.matches.map((match) => (
                        <MatchCard match={match} key={match.id} />
                      ))}
                    </ol>
                  </div>
                );
              })}
            </div>
          </section>
        );
      })}
    </>
  );
}
