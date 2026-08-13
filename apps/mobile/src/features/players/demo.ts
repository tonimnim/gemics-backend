import { PlayerMatch, PlayerForm, RankedPlayer, TournamentResult } from './types';

type PlayerSeed = Omit<RankedPlayer, 'matches' | 'tournaments'>;

const recentOpponents = [
  ['Amani Otieno', '@amani.ctrl'],
  ['Mercy Njeri', '@mercy.nj'],
  ['Kevin Omondi', '@kev.254'],
  ['Talia Muthoni', '@talia.press'],
  ['Ibrahim Noor', '@ibrahim.noor'],
] as const;

const events = ['Gamics Open #01', 'Nairobi Sunday Knockout', 'Coast Rising Stars'];

function makeMatches(playerIndex: number, form: PlayerForm[]): PlayerMatch[] {
  return form.map((result, index) => {
    const opponent = recentOpponents[(playerIndex + index) % recentOpponents.length];
    const won = result === 'W';
    const draw = result === 'D';

    return {
      id: `match-${playerIndex + 1}-${index + 1}`,
      opponent: opponent[0],
      opponentHandle: opponent[1],
      competition: events[(playerIndex + index) % events.length],
      playedAt: ['08 Aug 2026', '03 Aug 2026', '28 Jul 2026', '20 Jul 2026', '14 Jul 2026'][index],
      score: won ? (index % 2 === 0 ? '3–1' : '2–0') : draw ? '2–2' : index % 2 === 0 ? '1–2' : '0–1',
      result,
      ratingDelta: won ? 18 - index : draw ? 1 : -11 - index,
      verified: true,
    };
  });
}

function makeTournaments(playerIndex: number, nationalRank: number): TournamentResult[] {
  const leadingPlacement = Math.max(1, Math.min(16, Math.ceil(nationalRank / 3)));

  return [
    {
      id: `tournament-${playerIndex + 1}-1`,
      name: events[playerIndex % events.length],
      playedAt: 'Aug 2026',
      placement: leadingPlacement,
      entrants: 64,
      record: leadingPlacement <= 4 ? '5W · 1L' : '3W · 2L',
      format: 'Single elimination',
    },
    {
      id: `tournament-${playerIndex + 1}-2`,
      name: events[(playerIndex + 1) % events.length],
      playedAt: 'Jul 2026',
      placement: Math.min(32, leadingPlacement + 5),
      entrants: 128,
      record: '4W · 2L',
      format: 'Double elimination',
    },
  ];
}

const seeds: PlayerSeed[] = [
  { id: 'imani-v', name: 'Imani Wanjiku', handle: '@imani.v', avatarUrl: 'https://images.unsplash.com/photo-1534528741775-53994a69daeb?w=240&h=240&fit=crop', county: 'Nairobi', countryCode: 'KE', rating: 2148, nationalRank: 1, globalRank: 118, rankMovement: 2, wins: 92, losses: 17, draws: 4, winRate: 81, form: ['W', 'W', 'W', 'L', 'W'], joinedAt: 'February 2026' },
  { id: 'theo-254', name: 'Theo Kariuki', handle: '@theo.254', avatarUrl: 'https://images.unsplash.com/photo-1500648767791-00dcc994a43e?w=240&h=240&fit=crop', county: 'Kiambu', countryCode: 'KE', rating: 2105, nationalRank: 2, globalRank: 141, rankMovement: 0, wins: 88, losses: 21, draws: 3, winRate: 79, form: ['W', 'W', 'L', 'W', 'W'], joinedAt: 'January 2026' },
  { id: 'nash-k', name: 'Nashon Kiptoo', handle: '@nash.k', avatarUrl: 'https://images.unsplash.com/photo-1506794778202-cad84cf45f1d?w=240&h=240&fit=crop', county: 'Uasin Gishu', countryCode: 'KE', rating: 2079, nationalRank: 3, globalRank: 167, rankMovement: 1, wins: 76, losses: 19, draws: 6, winRate: 75, form: ['W', 'D', 'W', 'W', 'W'], joinedAt: 'March 2026' },
  { id: 'zawadi-press', name: 'Zawadi Achieng', handle: '@zawadi.press', avatarUrl: 'https://images.unsplash.com/photo-1524504388940-b1c1722653e1?w=240&h=240&fit=crop', county: 'Kisumu', countryCode: 'KE', rating: 2042, nationalRank: 4, globalRank: 205, rankMovement: -1, wins: 70, losses: 20, draws: 3, winRate: 75, form: ['L', 'W', 'W', 'W', 'W'], joinedAt: 'January 2026' },
  { id: 'malik-coast', name: 'Malik Hassan', handle: '@malik.coast', avatarUrl: 'https://images.unsplash.com/photo-1507003211169-0a1dd7228f2d?w=240&h=240&fit=crop', county: 'Mombasa', countryCode: 'KE', rating: 2017, nationalRank: 5, globalRank: 244, rankMovement: 3, wins: 68, losses: 22, draws: 2, winRate: 74, form: ['W', 'W', 'W', 'W', 'L'], joinedAt: 'April 2026' },
  { id: 'njeri-counter', name: 'Njeri Kamau', handle: '@njeri.counter', avatarUrl: 'https://images.unsplash.com/photo-1531123897727-8f129e1688ce?w=240&h=240&fit=crop', county: 'Nakuru', countryCode: 'KE', rating: 1996, nationalRank: 6, globalRank: 281, rankMovement: -2, wins: 64, losses: 23, draws: 5, winRate: 70, form: ['L', 'W', 'D', 'W', 'W'], joinedAt: 'March 2026' },
  { id: 'omar-zone', name: 'Omar Said', handle: '@omar.zone', avatarUrl: 'https://images.unsplash.com/photo-1507591064344-4c6ce005b128?w=240&h=240&fit=crop', county: 'Kilifi', countryCode: 'KE', rating: 1974, nationalRank: 7, globalRank: 326, rankMovement: 0, wins: 62, losses: 25, draws: 4, winRate: 68, form: ['W', 'L', 'W', 'D', 'W'], joinedAt: 'February 2026' },
  { id: 'wambo-10', name: 'Wambui Nduta', handle: '@wambo.10', avatarUrl: 'https://images.unsplash.com/photo-1488426862026-3ee34a7d66df?w=240&h=240&fit=crop', county: 'Nyeri', countryCode: 'KE', rating: 1948, nationalRank: 8, globalRank: 365, rankMovement: 4, wins: 60, losses: 25, draws: 6, winRate: 66, form: ['W', 'W', 'W', 'D', 'L'], joinedAt: 'May 2026' },
  { id: 'brian-m', name: 'Brian Maina', handle: '@brian.mainaa', avatarUrl: 'https://images.unsplash.com/photo-1501196354995-cbb51c65aaea?w=240&h=240&fit=crop', county: 'Nairobi', countryCode: 'KE', rating: 1922, nationalRank: 9, globalRank: 414, rankMovement: 1, wins: 55, losses: 19, draws: 3, winRate: 71, form: ['W', 'W', 'L', 'W', 'W'], joinedAt: 'January 2026' },
  { id: 'faith-builds', name: 'Faith Mumo', handle: '@faith.builds', avatarUrl: 'https://images.unsplash.com/photo-1544725176-7c40e5a71c5e?w=240&h=240&fit=crop', county: 'Machakos', countryCode: 'KE', rating: 1901, nationalRank: 10, globalRank: 447, rankMovement: -1, wins: 51, losses: 22, draws: 4, winRate: 66, form: ['L', 'W', 'W', 'D', 'W'], joinedAt: 'April 2026' },
  { id: 'kiongozi', name: 'Samuel Oduor', handle: '@kiongozi', avatarUrl: 'https://images.unsplash.com/photo-1531427186611-ecfd6d936c79?w=240&h=240&fit=crop', county: 'Siaya', countryCode: 'KE', rating: 1876, nationalRank: 11, globalRank: 492, rankMovement: 2, wins: 47, losses: 22, draws: 7, winRate: 62, form: ['W', 'D', 'W', 'L', 'W'], joinedAt: 'May 2026' },
  { id: 'naliaka-7', name: 'Lydia Naliaka', handle: '@naliaka.7', avatarUrl: 'https://images.unsplash.com/photo-1534751516642-a1af1ef26a56?w=240&h=240&fit=crop', county: 'Bungoma', countryCode: 'KE', rating: 1843, nationalRank: 12, globalRank: 537, rankMovement: 5, wins: 44, losses: 24, draws: 2, winRate: 63, form: ['W', 'W', 'W', 'L', 'W'], joinedAt: 'June 2026' },
  { id: 'chege-lab', name: 'Martin Chege', handle: '@chege.lab', avatarUrl: 'https://images.unsplash.com/photo-1519085360753-af0119f7cbe7?w=240&h=240&fit=crop', county: 'Murang’a', countryCode: 'KE', rating: 1819, nationalRank: 13, globalRank: 581, rankMovement: -3, wins: 42, losses: 25, draws: 4, winRate: 59, form: ['L', 'L', 'W', 'W', 'D'], joinedAt: 'February 2026' },
  { id: 'shiro-switch', name: 'Shiro Muthoni', handle: '@shiro.switch', avatarUrl: 'https://images.unsplash.com/photo-1529139574466-a303027c1d8b?w=240&h=240&fit=crop', county: 'Embu', countryCode: 'KE', rating: 1792, nationalRank: 14, globalRank: 628, rankMovement: 0, wins: 39, losses: 24, draws: 5, winRate: 57, form: ['D', 'W', 'L', 'W', 'W'], joinedAt: 'June 2026' },
  { id: 'dk-press', name: 'Dennis Kibet', handle: '@dk.press', avatarUrl: 'https://images.unsplash.com/photo-1504257432389-52343af06ae3?w=240&h=240&fit=crop', county: 'Kericho', countryCode: 'KE', rating: 1770, nationalRank: 15, globalRank: 672, rankMovement: 1, wins: 38, losses: 26, draws: 3, winRate: 57, form: ['W', 'L', 'W', 'L', 'W'], joinedAt: 'March 2026' },
  { id: 'aisha-nine', name: 'Aisha Ali', handle: '@aisha.nine', avatarUrl: 'https://images.unsplash.com/photo-1544005313-94ddf0286df2?w=240&h=240&fit=crop', county: 'Garissa', countryCode: 'KE', rating: 1749, nationalRank: 16, globalRank: 715, rankMovement: 2, wins: 36, losses: 27, draws: 4, winRate: 54, form: ['W', 'D', 'L', 'W', 'L'], joinedAt: 'July 2026' },
];

export const rankedPlayers: RankedPlayer[] = seeds.map((player, index) => ({
  ...player,
  matches: makeMatches(index, player.form),
  tournaments: makeTournaments(index, player.nationalRank),
}));

export function findPlayer(id: string | undefined) {
  return rankedPlayers.find((player) => player.id === id);
}
