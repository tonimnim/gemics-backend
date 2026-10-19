// Rendered on the server, so the pages ship no client JavaScript and need no
// CORS grant. Inside Docker the API is reachable as the compose service name;
// the localhost fallback is for `npm run dev` on a developer machine.
const apiURL = process.env.API_URL ?? "http://localhost:8080";

export type Competition = {
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
  single_elimination: "Knockout",
  double_elimination: "Double elimination",
  round_robin: "League",
};

const statusLabels: Record<string, string> = {
  published: "Announced",
  registration_open: "Registration open",
  check_in: "Check-in open",
  running: "Underway",
  completed: "Finished",
};

export const formatLabel = (format: string) => formatLabels[format] ?? format;
export const statusLabel = (status: string) => statusLabels[status] ?? status;

/**
 * Entry fees are integer minor units in the competition's currency, and not
 * every currency has cents (JPY has none), so the divisor follows the currency.
 */
export function entryLabel(competition: Competition) {
  if (competition.entryType !== "paid") return "Free";
  const currency = competition.currency || "KES";
  try {
    const format = new Intl.NumberFormat("en", { style: "currency", currency, currencyDisplay: "code" });
    const digits = format.resolvedOptions().maximumFractionDigits ?? 2;
    const amount = competition.entryFeeMinor / 10 ** digits;
    return new Intl.NumberFormat("en", {
      style: "currency",
      currency,
      currencyDisplay: "code",
      minimumFractionDigits: Number.isInteger(amount) ? 0 : digits,
    }).format(amount);
  } catch {
    return `${currency} ${competition.entryFeeMinor / 100}`;
  }
}

export function startLabel(value: string) {
  const startsAt = new Date(value);
  if (Number.isNaN(startsAt.getTime())) return "Date to be confirmed";
  return startsAt.toLocaleDateString("en-GB", { day: "numeric", month: "short" });
}

/**
 * Calendar parts for a date block. Rendered in UTC so every server agrees;
 * the browser re-renders `[data-local-time]` elements in the visitor's zone.
 */
export function dateParts(value: string) {
  const at = new Date(value);
  if (Number.isNaN(at.getTime())) return null;
  const part = (options: Intl.DateTimeFormatOptions) => at.toLocaleString("en-GB", { timeZone: "UTC", ...options });
  return {
    iso: at.toISOString(),
    month: part({ month: "short" }),
    day: part({ day: "numeric" }),
    weekday: part({ weekday: "short" }),
    time: `${part({ hour: "2-digit", minute: "2-digit", hour12: false })} UTC`,
  };
}

/** Whole days, hours, minutes and seconds until `value`, floored at zero. */
export function countdownParts(value: string, now = new Date()) {
  const remaining = Math.max(0, new Date(value).getTime() - now.getTime());
  const seconds = Math.floor(remaining / 1000);
  return {
    days: Math.floor(seconds / 86400),
    hours: Math.floor((seconds % 86400) / 3600),
    minutes: Math.floor((seconds % 3600) / 60),
    seconds: seconds % 60,
  };
}

export function fillPercent(competition: Competition) {
  return Math.min(100, Math.round((competition.entryCount / Math.max(1, competition.maxEntries)) * 100));
}

// Joinable first, then upcoming, then underway, finished last. The sort is
// stable, so the API's start-date order holds within each group.
const statusOrder = ["registration_open", "check_in", "published", "running", "completed"];

function statusRank(status: string) {
  const rank = statusOrder.indexOf(status);
  return rank === -1 ? statusOrder.length : rank;
}

/** The public competition list. An unreachable API reads as "nothing to show". */
export async function loadCompetitions(): Promise<{ data: Competition[]; reachable: boolean }> {
  try {
    // no-store because a tournament that just opened should appear immediately;
    // the API already fronts this query with its own short-lived cache.
    const response = await fetch(`${apiURL}/v1/competitions?limit=50`, { cache: "no-store" });
    if (!response.ok) return { data: [], reachable: false };
    const body = (await response.json()) as { data?: Competition[] };
    const data = [...(body.data ?? [])].sort((a, b) => statusRank(a.status) - statusRank(b.status));
    return { data, reachable: true };
  } catch {
    return { data: [], reachable: false };
  }
}

export type MatchFormat = { bestOf: number; rounds?: Record<string, number> };

/** One competition as GET /v1/competitions/{id} returns it. */
export type CompetitionDetail = Competition & {
  prizeAmountMinor: number;
  prizeFunding: string;
  registrationOpensAt: string;
  registrationClosesAt: string;
  checkInOpensAt: string | null;
  matchFormat: MatchFormat;
};

export type BracketSide = {
  side: "home" | "away";
  participant: { entryId: string; displayName: string; seed: number | null } | null;
};

export type BracketMatch = {
  id: string;
  code: string;
  matchNumber: number;
  bestOf: number;
  state: string;
  home: BracketSide;
  away: BracketSide;
  score: { homeScore: number; awayScore: number; tiebreak: { homeScore: number; awayScore: number } | null } | null;
  winnerEntryId: string | null;
  scheduledAt: string | null;
};

export type BracketRound = { number: number; name: string; bracket: string; matches: BracketMatch[] };
export type BracketStage = { id: string; name: string; format: string; status: string; rounds: BracketRound[] };

export type StandingRow = {
  rank: number | null;
  entryId: string;
  displayName: string;
  played: number;
  wins: number;
  draws: number;
  losses: number;
  gamesWon: number;
  gamesLost: number;
  goalsFor: number;
  goalsAgainst: number;
  goalDifference: number;
  points: number;
  removed: boolean;
};

export type Standings = {
  stages: { id: string; name: string; groups: { key: string; rows: StandingRow[] }[] }[];
  placements: { placement: number; entryId: string; displayName: string }[];
};

export type Loaded<T> = { state: "ok"; data: T } | { state: "missing" } | { state: "unavailable" };

const uuid = /^[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}$/i;

/**
 * Reads one public API document. Only the API's own "…_not_found" answer means
 * the thing doesn't exist; any other failure, including a 404 from something
 * that isn't the API, is "unavailable".
 */
export async function loadPublic<T>(path: string, { enveloped = true } = {}): Promise<Loaded<T>> {
  try {
    const response = await fetch(`${apiURL}${path}`, { cache: "no-store" });
    if (response.status === 404) {
      const body = (await response.json().catch(() => null)) as { error?: string } | null;
      return /_not_found$/.test(body?.error ?? "") ? { state: "missing" } : { state: "unavailable" };
    }
    if (!response.ok) return { state: "unavailable" };
    const body = (await response.json()) as { data?: T } & T;
    // Most documents come as {"data": …}; a public player profile comes bare.
    const data = enveloped ? body.data : (body as T);
    return data === undefined || data === null ? { state: "unavailable" } : { state: "ok", data };
  } catch {
    return { state: "unavailable" };
  }
}

export async function loadCompetition(id: string): Promise<Loaded<CompetitionDetail>> {
  if (!uuid.test(id)) return { state: "missing" };
  return loadPublic<CompetitionDetail>(`/v1/competitions/${id}`);
}

export async function loadBracket(id: string): Promise<BracketStage[] | null> {
  const result = await loadPublic<{ stages: BracketStage[] }>(`/v1/competitions/${id}/bracket`);
  return result.state === "ok" ? result.data.stages : null;
}

export async function loadStandings(id: string): Promise<Standings | null> {
  const result = await loadPublic<Standings>(`/v1/competitions/${id}/standings`);
  return result.state === "ok" ? result.data : null;
}

/** A money amount in the competition's currency, in whole units where it has no fraction. */
export function moneyLabel(amountMinor: number, currency: string) {
  return entryLabel({ entryType: "paid", entryFeeMinor: amountMinor, currency } as Competition);
}

const roundNames: Record<string, string> = {
  final: "Final",
  semifinal: "Semifinal",
  quarterfinal: "Quarterfinal",
  third_place: "Third place",
  winners_final: "Winners final",
  losers_final: "Losers final",
};

/** "Best of 1 · Semifinal best of 3 · Final best of 5" */
export function matchFormatLabel(format: MatchFormat | undefined) {
  const bestOf = format?.bestOf ?? 1;
  const overrides = Object.entries(format?.rounds ?? {}).map(
    ([round, value]) => `${roundNames[round] ?? round.replace(/_/g, " ").replace(/^\w/, (c) => c.toUpperCase())} best of ${value}`,
  );
  return [`Best of ${bestOf}`, ...overrides].join(" · ");
}
