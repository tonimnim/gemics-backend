// Server-side reads of the public leaderboard. API_URL is the address this
// server can reach; NEXT_PUBLIC_API_URL is the one a browser can, which is what
// avatar images need because they load from the visitor's device.
const apiURL = process.env.API_URL ?? "http://localhost:8080";
const publicApiURL = process.env.NEXT_PUBLIC_API_URL ?? "";

export type RankedPlayer = {
  rank: number;
  playerId: string;
  handle: string;
  displayName: string;
  avatarUrl: string | null;
  countryCode: string;
  rating: number;
  matchesPlayed: number;
  /** Places gained since the previous snapshot; negative means dropped. */
  rankMovement: number;
};

/** The launch markets, in rollout order. Anything else falls back to global. */
export const rankingCountries = [
  { code: "KE", name: "Kenya" },
  { code: "IN", name: "India" },
  { code: "SG", name: "Singapore" },
  { code: "ID", name: "Indonesia" },
  { code: "BR", name: "Brazil" },
  { code: "JP", name: "Japan" },
] as const;

export function rankingCountry(code: string | undefined) {
  const wanted = (code ?? "").toUpperCase();
  return rankingCountries.find((country) => country.code === wanted) ?? null;
}

export type RankingsResult =
  | { state: "ok"; data: RankedPlayer[]; nextCursor: string | null; snapshotAt: string | null }
  | { state: "expired" }
  | { state: "unavailable" };

export async function loadRankings(countryCode: string | null, cursor: string | null): Promise<RankingsResult> {
  const query = new URLSearchParams({ limit: "50" });
  if (countryCode) {
    query.set("scope", "country");
    query.set("country", countryCode);
  } else {
    query.set("scope", "global");
  }
  if (cursor) query.set("cursor", cursor);

  try {
    // no-store: the API serves an immutable snapshot per page and caches it
    // itself, and a fresh snapshot should show up on the next visit.
    const response = await fetch(`${apiURL}/v1/rankings?${query}`, { cache: "no-store" });
    if (response.status === 410 || (response.status === 400 && cursor)) return { state: "expired" };
    if (!response.ok) return { state: "unavailable" };
    const body = (await response.json()) as {
      data?: RankedPlayer[];
      page?: { nextCursor: string | null; hasMore: boolean };
      snapshotAt?: string | null;
    };
    return {
      state: "ok",
      data: body.data ?? [],
      nextCursor: body.page?.hasMore ? body.page.nextCursor : null,
      snapshotAt: body.snapshotAt ?? null,
    };
  } catch {
    return { state: "unavailable" };
  }
}

/** Absolute avatar address, or null when there is none or no browser-facing API to load it from. */
export function avatarSource(player: RankedPlayer) {
  if (!player.avatarUrl) return null;
  if (/^https?:\/\//.test(player.avatarUrl)) return player.avatarUrl;
  return publicApiURL ? `${publicApiURL.replace(/\/$/, "")}${player.avatarUrl}` : null;
}

export function initials(player: RankedPlayer) {
  const source = player.displayName.trim() || player.handle;
  const parts = source.split(/\s+/).filter(Boolean);
  const letters = parts.length > 1 ? parts[0][0] + parts[parts.length - 1][0] : source.slice(0, 2);
  return letters.toUpperCase();
}

/** A stable hue offset per player, so initials tiles are told apart without being random per render. */
export function avatarTone(playerId: string) {
  let hash = 0;
  for (const character of playerId) hash = (hash * 31 + character.charCodeAt(0)) | 0;
  return Math.abs(hash) % 4;
}

export function updatedLabel(snapshotAt: string | null, now = new Date()) {
  if (!snapshotAt) return null;
  const at = new Date(snapshotAt);
  if (Number.isNaN(at.getTime())) return null;
  const minutes = Math.max(0, Math.round((now.getTime() - at.getTime()) / 60000));
  if (minutes < 1) return "Updated just now";
  if (minutes < 60) return `Updated ${minutes} min ago`;
  const hours = Math.round(minutes / 60);
  if (hours < 24) return `Updated ${hours} h ago`;
  return `Updated ${at.toLocaleDateString("en-GB", { day: "numeric", month: "short" })}`;
}
