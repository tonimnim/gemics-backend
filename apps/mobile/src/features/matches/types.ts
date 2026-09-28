export type BackendCapabilityStatus = 'available' | 'demo' | 'unavailable';

export type MatchCapability =
  | 'match_detail'
  | 'check_in'
  | 'signed_evidence_upload'
  | 'score_report'
  | 'final_score_report';

export type MatchCapabilityMap = Record<MatchCapability, BackendCapabilityStatus>;

/**
 * Server-derived viewer lifecycle. `awaiting_resolution` covers the short gap
 * between a passed deadline and the server settling the match; it offers no
 * action.
 */
export type MatchLifecycle =
  | 'assigned'
  | 'ready_for_check_in'
  | 'checked_in'
  | 'report_required'
  | 'awaiting_opponent_report'
  | 'mismatch_response_required'
  | 'awaiting_opponent_response'
  | 'awaiting_resolution'
  | 'under_review'
  | 'forfeited'
  | 'completed';

export type ParticipantSide = 'home' | 'away';

export type MatchAllowedAction = 'check_in' | 'report_score' | 'submit_final_score';

export type MatchParticipant = {
  playerId: string;
  handle: string;
  displayName: string;
  initials: string;
  avatarURL?: string;
  side: ParticipantSide;
  checkedInAt?: string;
};

export type MatchScore = {
  home: number;
  away: number;
  tiebreak?: {
    type: 'penalties';
    home: number;
    away: number;
  };
};

export type GameScore = { homeScore: number; awayScore: number };

export type ScoreTiebreak = { type: 'penalties'; homeScore: number; awayScore: number };

/**
 * One of the viewer's own score reports, in the wire shape of the match room.
 * The app never receives the opponent's claim.
 */
export type ScoreReport = {
  id: string;
  kind: 'initial' | 'final';
  homeScore: number;
  awayScore: number;
  tiebreak: ScoreTiebreak | null;
  games: GameScore[];
  /** Present only on a final report. */
  evidenceIds?: string[];
  reportedAt: string;
};

export type ResultVerificationPhase =
  | 'not_started'
  | 'awaiting_second_report'
  | 'awaiting_responses'
  | 'in_review'
  | 'resolved';

export type ResultVerificationResolution =
  | 'agreed'
  | 'report_timeout'
  | 'response_timeout'
  | 'platform_review'
  | 'competition_cancelled';

/**
 * The viewer's blind view of result verification. About the opponent it says
 * only whether they reported or responded; each deadline is set only in the
 * phase it governs.
 */
export type ResultVerification = {
  phase: ResultVerificationPhase;
  myReport: ScoreReport | null;
  myFinalReport: ScoreReport | null;
  opponentReported: boolean;
  opponentResponded: boolean;
  reportDeadline: string | null;
  responseDeadline: string | null;
  resolution: ResultVerificationResolution | null;
  entryRemoved: boolean;
};

export type MatchEvidence = {
  id: string;
  fileName: string;
  contentType: string;
  previewURL?: string;
  uploadedAt?: string;
};

export type MatchResultOrigin = 'agreed_reports' | 'platform_review' | 'legacy';

/** The canonical confirmed score, present only once the match is completed. */
export type MatchResult = {
  homeScore: number;
  awayScore: number;
  tiebreak: ScoreTiebreak | null;
  origin: MatchResultOrigin;
  confirmedAt: string;
};

/**
 * Mobile view model used by the demo repository. It is not a claimed wire schema;
 * the production adapter must map the generated OpenAPI match detail into it.
 */
export type MatchRoom = {
  id: string;
  code: string;
  competitionId: string;
  competitionName: string;
  gameName: string;
  stageName: string;
  roundName: string;
  bestOf: number;
  drawAllowed: boolean;
  scheduledAt: string;
  checkInClosesAt: string;
  lifecycle: MatchLifecycle;
  allowedActions: MatchAllowedAction[];
  friendMatchInstructions: Array<{ title: string; detail: string }>;
  currentPlayerId: string;
  currentPlayerSide: ParticipantSide;
  home: MatchParticipant;
  away: MatchParticipant;
  resultVerification: ResultVerification;
  result: MatchResult | null;
};

export type MatchSummary = Pick<
  MatchRoom,
  | 'id'
  | 'code'
  | 'competitionName'
  | 'gameName'
  | 'roundName'
  | 'scheduledAt'
  | 'lifecycle'
  | 'allowedActions'
  | 'currentPlayerSide'
  | 'home'
  | 'away'
>;

export type MatchHistoryItem = {
  matchId: string;
  competitionName: string;
  opponent: MatchParticipant;
  score: MatchScore;
  outcome: 'win' | 'loss' | 'draw';
  result: Pick<MatchResult, 'origin' | 'confirmedAt'>;
  ratingDelta: number;
};

export type LocalEvidenceAsset = {
  uri: string;
  fileName: string;
  contentType: string;
  byteSize?: number;
  width: number;
  height: number;
};

export type PreparedEvidenceUpload = Omit<LocalEvidenceAsset, 'byteSize'> & {
  byteSize: number;
  sha256: string;
};

export type EvidenceUploadIntent = {
  id: string;
  uploadUrl: string;
  requiredHeaders: Record<string, string>;
  expiresAt: string;
};

/** A blind initial report: score only, no screenshot. */
export type ScoreReportInput = {
  idempotencyKey: string;
  homeScore: number;
  awayScore: number;
  tiebreak?: ScoreTiebreak;
  /** Optional; the server records one aggregate game when omitted. */
  games?: GameScore[];
  declarationAccepted: true;
};

/** The one final score after a mismatch, with one to three ready screenshots. */
export type FinalScoreReportInput = ScoreReportInput & {
  evidenceIds: string[];
};

/** The caller's updated blind room and the report just stored. */
export type ScoreReportOutcome = {
  match: MatchRoom;
  report: ScoreReport;
};

export interface MatchRepository {
  readonly capabilities: MatchCapabilityMap;
  getMatch(matchId: string, signal?: AbortSignal): Promise<MatchRoom>;
  checkIn(matchId: string, idempotencyKey: string): Promise<MatchRoom>;
  createEvidenceUploadIntent(asset: PreparedEvidenceUpload): Promise<EvidenceUploadIntent>;
  uploadEvidence(intent: EvidenceUploadIntent, asset: LocalEvidenceAsset): Promise<void>;
  completeEvidenceUpload(evidenceId: string): Promise<MatchEvidence>;
  reportScore(matchId: string, input: ScoreReportInput): Promise<ScoreReportOutcome>;
  submitFinalScore(matchId: string, input: FinalScoreReportInput): Promise<ScoreReportOutcome>;
}
