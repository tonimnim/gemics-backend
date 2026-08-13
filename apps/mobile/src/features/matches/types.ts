export type BackendCapabilityStatus = 'available' | 'demo' | 'unavailable';

export type MatchCapability =
  | 'match_detail'
  | 'check_in'
  | 'signed_evidence_upload'
  | 'result_submission'
  | 'result_decision';

export type MatchCapabilityMap = Record<MatchCapability, BackendCapabilityStatus>;

export type MatchLifecycle =
  | 'assigned'
  | 'ready_for_check_in'
  | 'checked_in'
  | 'opponent_action_required'
  | 'awaiting_opponent'
  | 'confirmed'
  | 'disputed'
  | 'under_review'
  | 'completed'
  | 'forfeited';

export type ParticipantSide = 'home' | 'away';

export type MatchAllowedAction = 'check_in' | 'submit_result' | 'confirm_result' | 'dispute_result';

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

export type MatchEvidence = {
  id: string;
  fileName: string;
  contentType: string;
  previewURL?: string;
  uploadedAt?: string;
};

export type MatchResult = {
  id: string;
  submittedByPlayerId: string;
  score: MatchScore;
  evidence: MatchEvidence[];
  declarationAcceptedAt: string;
  submittedAt: string;
  status: 'pending_confirmation' | 'confirmed' | 'disputed' | 'under_review';
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
  result?: MatchResult;
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
> & {
  result?: Pick<MatchResult, 'score' | 'status'>;
};

export type MatchHistoryItem = {
  matchId: string;
  competitionName: string;
  opponent: MatchParticipant;
  score: MatchScore;
  outcome: 'win' | 'loss' | 'draw';
  resultStatus: 'confirmed' | 'disputed' | 'forfeit';
  playedAt: string;
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

export type SubmitMatchResultInput = {
  idempotencyKey: string;
  homeScore: number;
  awayScore: number;
  tiebreak?: { type: 'penalties'; homeScore: number; awayScore: number };
  games: Array<{ homeScore: number; awayScore: number }>;
  evidenceIds: string[];
  declarationAccepted: true;
};

export type ResultDecisionInput =
  | { idempotencyKey: string; decision: 'confirm' }
  | {
      idempotencyKey: string;
      decision: 'dispute';
      reasonCode: 'score_mismatch' | 'invalid_evidence' | 'match_not_played' | 'other';
      note: string;
      evidenceIds: string[];
    };

export interface MatchRepository {
  readonly capabilities: MatchCapabilityMap;
  getMatch(matchId: string, signal?: AbortSignal): Promise<MatchRoom>;
  checkIn(matchId: string, idempotencyKey: string): Promise<MatchRoom>;
  createEvidenceUploadIntent(asset: PreparedEvidenceUpload): Promise<EvidenceUploadIntent>;
  uploadEvidence(intent: EvidenceUploadIntent, asset: LocalEvidenceAsset): Promise<void>;
  completeEvidenceUpload(evidenceId: string): Promise<MatchEvidence>;
  submitResult(matchId: string, input: SubmitMatchResultInput): Promise<MatchResult>;
  decideResult(submissionId: string, input: ResultDecisionInput): Promise<{ match: MatchRoom; submission: MatchResult }>;
}
