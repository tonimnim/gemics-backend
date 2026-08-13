export type PlayerForm = 'W' | 'D' | 'L';

export type PlayerMatch = {
  id: string;
  opponent: string;
  opponentHandle: string;
  competition: string;
  playedAt: string;
  score: string;
  result: PlayerForm;
  ratingDelta: number;
  verified: boolean;
};

export type TournamentResult = {
  id: string;
  name: string;
  playedAt: string;
  placement: number;
  entrants: number;
  record: string;
  format: string;
};

export type RankedPlayer = {
  id: string;
  name: string;
  handle: string;
  avatarUrl: string;
  county: string;
  countryCode: string;
  rating: number;
  nationalRank: number;
  globalRank: number;
  rankMovement: number;
  wins: number;
  losses: number;
  draws: number;
  winRate: number;
  form: PlayerForm[];
  joinedAt: string;
  matches: PlayerMatch[];
  tournaments: TournamentResult[];
};

export type RankingScope = 'national' | 'global';
export type RankingSort = 'rank' | 'rating' | 'wins';
