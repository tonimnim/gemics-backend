import type { MatchHistoryItem, MatchRoom, MatchSummary } from './types';

const brian = {
  playerId: 'player-brian',
  handle: '@brian.mainaa',
  displayName: 'Brian Maina',
  initials: 'BM',
} as const;

const slick = {
  playerId: 'player-slick',
  handle: '@slick.ken',
  displayName: 'Slick Ken',
  initials: 'SK',
} as const;

const pressing = {
  playerId: 'player-pressing',
  handle: '@pressingking',
  displayName: 'Pressing King',
  initials: 'PK',
} as const;

export const demoMatchRooms: Record<string, MatchRoom> = {
  'match-024': {
    id: 'match-024',
    code: 'M-024',
    competitionId: 'nairobi-sunday',
    competitionName: 'Nairobi Sunday Knockout',
    gameName: 'eFootball Mobile',
    stageName: 'Main bracket',
    roundName: 'Round 2',
    bestOf: 1,
    drawAllowed: false,
    scheduledAt: '2026-08-09T18:30:00+03:00',
    checkInClosesAt: '2026-08-09T18:40:00+03:00',
    lifecycle: 'ready_for_check_in',
    allowedActions: ['check_in'],
    friendMatchInstructions: [
      { title: 'Check in on Gamics', detail: 'Wait until both players show as ready.' },
      { title: 'Create the eFootball room', detail: 'Open Friend Match and use the tournament settings.' },
      { title: 'Finish the game', detail: 'Play the complete match. Do not leave the final screen.' },
      { title: 'Capture the result', detail: 'Keep both handles and the final score visible.' },
    ],
    currentPlayerId: brian.playerId,
    currentPlayerSide: 'home',
    home: { ...brian, side: 'home' },
    away: { ...slick, side: 'away' },
  },
  'match-025': {
    id: 'match-025',
    code: 'M-025',
    competitionId: 'coast-rising',
    competitionName: 'Coast Rising Stars',
    gameName: 'eFootball Mobile',
    stageName: 'Group B',
    roundName: 'Matchday 3',
    bestOf: 1,
    drawAllowed: true,
    scheduledAt: '2026-08-09T16:00:00+03:00',
    checkInClosesAt: '2026-08-09T16:10:00+03:00',
    lifecycle: 'opponent_action_required',
    allowedActions: ['confirm_result', 'dispute_result'],
    friendMatchInstructions: [
      { title: 'Review the submitted score', detail: 'Compare it with the match you played.' },
      { title: 'Inspect the screenshot', detail: 'Both handles and the final score should be visible.' },
    ],
    currentPlayerId: brian.playerId,
    currentPlayerSide: 'away',
    home: { ...pressing, side: 'home', checkedInAt: '2026-08-09T15:55:00+03:00' },
    away: { ...brian, side: 'away', checkedInAt: '2026-08-09T15:56:00+03:00' },
    result: {
      id: 'result-025',
      submittedByPlayerId: pressing.playerId,
      score: { home: 2, away: 1 },
      evidence: [
        {
          id: 'evidence-025',
          fileName: 'final-result-m025.jpg',
          contentType: 'image/jpeg',
          uploadedAt: '2026-08-09T16:31:00+03:00',
        },
      ],
      declarationAcceptedAt: '2026-08-09T16:32:00+03:00',
      submittedAt: '2026-08-09T16:32:00+03:00',
      status: 'pending_confirmation',
    },
  },
};

export const demoAssignedMatches: MatchSummary[] = Object.values(demoMatchRooms).map((match) => ({
  id: match.id,
  code: match.code,
  competitionName: match.competitionName,
  gameName: match.gameName,
  roundName: match.roundName,
  scheduledAt: match.scheduledAt,
  lifecycle: match.lifecycle,
  allowedActions: match.allowedActions,
  currentPlayerSide: match.currentPlayerSide,
  home: match.home,
  away: match.away,
  result: match.result ? { score: match.result.score, status: match.result.status } : undefined,
}));

export const demoMatchHistory: MatchHistoryItem[] = [
  {
    matchId: 'match-021',
    competitionName: 'Nairobi Sunday Knockout',
    opponent: { ...slick, side: 'away' },
    score: { home: 3, away: 1 },
    outcome: 'win',
    resultStatus: 'confirmed',
    playedAt: '2026-08-08T19:14:00+03:00',
    ratingDelta: 18,
  },
  {
    matchId: 'match-018',
    competitionName: 'Gamics Open #01',
    opponent: { playerId: 'player-tiki', handle: '@tiki_taka254', displayName: 'Tiki Taka', initials: 'TT', side: 'home' },
    score: { home: 0, away: 2 },
    outcome: 'win',
    resultStatus: 'confirmed',
    playedAt: '2026-08-02T14:20:00+03:00',
    ratingDelta: 21,
  },
  {
    matchId: 'match-011',
    competitionName: 'Coast Rising Stars',
    opponent: { playerId: 'player-mombasa', handle: '@mombasa10', displayName: 'Mombasa 10', initials: 'M1', side: 'away' },
    score: { home: 1, away: 2 },
    outcome: 'loss',
    resultStatus: 'confirmed',
    playedAt: '2026-07-27T12:45:00+03:00',
    ratingDelta: -12,
  },
];

export function getDemoMatchRoom(matchId: string | undefined): MatchRoom {
  return demoMatchRooms[matchId ?? 'match-024'] ?? demoMatchRooms['match-024'];
}
