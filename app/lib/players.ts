import { type Loaded, loadPublic } from "./competitions";

export type PublicPlayer = {
  playerId: string;
  handle: string;
  displayName: string;
  avatarUrl: string | null;
  countryCode: string;
  joinedAt: string;
  record: { matchesPlayed: number; wins: number; draws: number; losses: number };
  ratings: {
    gameId: string;
    rating: number;
    matchesPlayed: number;
    globalRank: number | null;
    countryRank: number | null;
  }[];
};

export type PlayerMatch = {
  matchId: string;
  playedAt: string;
  opponent: { playerId: string; handle: string; displayName: string } | null;
  score: { player: number; opponent: number; tiebreak?: { home: number; away: number } };
  outcome: string;
  competition: { competitionId: string; name: string };
  stage: string;
  roundNumber: number;
};

export type PlayerCompetition = {
  competitionId: string;
  name: string;
  format: string;
  status: string;
  startedAt: string;
  placement: number | null;
  entrants: number;
  record: { matchesPlayed: number; wins: number; draws: number; losses: number };
};

const uuid = /^[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}$/i;

export async function loadPlayer(id: string): Promise<Loaded<PublicPlayer>> {
  if (!uuid.test(id)) return { state: "missing" };
  return loadPublic<PublicPlayer>(`/v1/players/${id}`, { enveloped: false });
}

export async function loadPlayerMatches(id: string): Promise<PlayerMatch[]> {
  const result = await loadPublic<PlayerMatch[]>(`/v1/players/${id}/matches?limit=10`);
  return result.state === "ok" ? result.data : [];
}

export async function loadPlayerCompetitions(id: string): Promise<PlayerCompetition[]> {
  const result = await loadPublic<PlayerCompetition[]>(`/v1/players/${id}/competitions?limit=10`);
  return result.state === "ok" ? result.data : [];
}
