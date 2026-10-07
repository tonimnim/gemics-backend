export type ReviewStatus = 'queued' | 'decided' | 'closed'
export type ReviewDecision =
  'accept_home' | 'accept_away' | 'corrected_score' | 'remove_both'

type Score = {
  homeScore: number
  awayScore: number
  tiebreak?: { type: 'penalties'; homeScore: number; awayScore: number } | null
}

export type ReviewMatch = {
  id: string
  competitionId: string
  competitionName: string
  stageFormat: string
  bracket: string
  roundNumber: number
  matchNumber: number
  bestOf: number
}

export type ReviewSummary = {
  id: string
  status: ReviewStatus
  reason: 'reports_differ' | 'evidence_unavailable'
  version: number
  queuedAt: string
  decision: ReviewDecision | null
  decidedAt: string | null
  match: ReviewMatch
}

export type StaffReport = Score & {
  id: string
  reportedBy: { userId: string; displayName: string }
  reportedAt: string
  evidence: {
    id: string
    mediaType: string
    byteSize: number
    ready: boolean
  }[]
}

type Participant = {
  entryId: string
  entryStatus: string
  captainUserId: string
  displayName: string
  handle: string | null
}

export type ReviewDetail = Omit<ReviewSummary, 'decision'> & {
  decision: {
    decision: ReviewDecision
    correctedScore: Score | null
    note: string
    deciderKind: 'staff' | 'system'
    decidedAt: string
  } | null
  participants: { home: Participant; away: Participant }
  reports: {
    home: { initial: StaffReport | null; final: StaffReport | null }
    away: { initial: StaffReport | null; final: StaffReport | null }
  }
  verification: { mismatchAt: string | null; responseDeadlineAt: string | null }
  activeStrikeCounts: Record<string, number>
}

export function scoreText(score: Score) {
  const penalties = score.tiebreak
    ? ` (${score.tiebreak.homeScore}-${score.tiebreak.awayScore} pens)`
    : ''
  return `${score.homeScore} - ${score.awayScore}${penalties}`
}
