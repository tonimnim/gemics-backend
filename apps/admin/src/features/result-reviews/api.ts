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

/**
 * The submitted result ("initial", with its score) or a screenshot sent after
 * a rejection ("final", no score).
 */
export type StaffReport = {
  id: string
  homeScore: number | null
  awayScore: number | null
  tiebreak?: { type: 'penalties'; homeScore: number; awayScore: number } | null
  reportedBy: { userId: string; displayName: string }
  reportedAt: string
  evidence: {
    id: string
    mediaType: string
    byteSize: number
    ready: boolean
    reading: ScreenshotReading | null
  }[]
}

/** A result in match orientation, from the screenshot reader. */
export type ScreenshotScore = {
  homeScore: number
  awayScore: number
  homePenalties: number | null
  awayPenalties: number | null
}

/** What the screenshot reader made of one screenshot. */
export type ScreenshotReading = {
  status: 'queued' | 'read' | 'failed'
  screen: 'match_result' | 'unknown' | null
  confidence: number | null
  left: { team: string; score: number } | null
  right: { team: string; score: number } | null
  penalties: { left: number; right: number } | null
  stats: Record<string, [number, number]>
  orientation: 'home_left' | 'home_right' | 'either' | 'unknown'
  score: ScreenshotScore | null
  flags: string[]
  reusedMatchId: string | null
  error: string | null
  model: string | null
}

/** The reader's verdict on the whole review. */
export type ScreenshotCheck = {
  verdict:
    | 'pending'
    | 'supports'
    | 'neither'
    | 'conflicting'
    | 'unreadable'
    | 'inconclusive'
  score: ScreenshotScore | null
  differences: string[]
  reasons: string[]
  /** The side whose submitted result the screenshots match. */
  supportedSide?: 'home' | 'away'
  decision?: 'accept_home' | 'accept_away'
  /** The reader will settle this review itself. */
  autoDecide: boolean
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
  verification: {
    firstReportEntryId: string
    mismatchAt: string | null
    responseDeadlineAt: string | null
    /** The player who rejected the submitted result. */
    rejectedBy: string | null
  }
  activeStrikeCounts: Record<string, number>
  screenshotCheck: ScreenshotCheck | null
}

export function scoreText(score: Score) {
  const penalties = score.tiebreak
    ? ` (${score.tiebreak.homeScore}-${score.tiebreak.awayScore} pens)`
    : ''
  return `${score.homeScore} - ${score.awayScore}${penalties}`
}
