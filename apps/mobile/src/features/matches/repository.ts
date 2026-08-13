import { demoMatchRooms, getDemoMatchRoom } from './demo';
import type { MatchCapability, MatchRepository } from './types';

export class DemoMatchBoundaryError extends Error {
  constructor(public readonly capability: MatchCapability) {
    super(`${capability} cannot cross the demo repository boundary`);
    this.name = 'DemoMatchBoundaryError';
  }
}

/**
 * Explicit local adapter for design/testing. Evidence never pretends to upload;
 * production must inject the capability-gated HTTP repository instead.
 */
export const demoMatchRepository: MatchRepository = {
  capabilities: {
    match_detail: 'demo',
    check_in: 'demo',
    signed_evidence_upload: 'unavailable',
    result_submission: 'demo',
    result_decision: 'demo',
  },
  async getMatch(matchId) {
    return getDemoMatchRoom(matchId);
  },
  async checkIn(matchId) {
    const match = getDemoMatchRoom(matchId);
    return { ...match, lifecycle: 'checked_in', allowedActions: ['submit_result'] };
  },
  async createEvidenceUploadIntent() {
    throw new DemoMatchBoundaryError('signed_evidence_upload');
  },
  async uploadEvidence() {
    throw new DemoMatchBoundaryError('signed_evidence_upload');
  },
  async completeEvidenceUpload() {
    throw new DemoMatchBoundaryError('signed_evidence_upload');
  },
  async submitResult(matchId, input) {
    const match = getDemoMatchRoom(matchId);
    return {
      id: `demo-submission-${matchId}`,
      submittedByPlayerId: match.currentPlayerId,
      score: {
        home: input.homeScore,
        away: input.awayScore,
        tiebreak: input.tiebreak
          ? { type: input.tiebreak.type, home: input.tiebreak.homeScore, away: input.tiebreak.awayScore }
          : undefined,
      },
      evidence: [],
      declarationAcceptedAt: new Date().toISOString(),
      submittedAt: new Date().toISOString(),
      status: 'pending_confirmation',
    };
  },
  async decideResult(submissionId, input) {
    const match = Object.values(demoMatchRooms).find((candidate) => candidate.result?.id === submissionId)
      ?? getDemoMatchRoom(undefined);
    const submission = match.result ?? {
      id: submissionId,
      submittedByPlayerId: match.currentPlayerId,
      score: { home: 0, away: 0 },
      evidence: [],
      declarationAcceptedAt: new Date().toISOString(),
      submittedAt: new Date().toISOString(),
      status: 'pending_confirmation' as const,
    };
    const nextStatus = input.decision === 'confirm' ? 'confirmed' as const : 'disputed' as const;
    return {
      match: { ...match, lifecycle: input.decision === 'confirm' ? 'confirmed' : 'under_review' },
      submission: { ...submission, status: nextStatus },
    };
  },
};
