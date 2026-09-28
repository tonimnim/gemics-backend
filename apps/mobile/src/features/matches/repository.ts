import { getDemoMatchRoom } from './demo';
import type { MatchCapability, MatchRepository, MatchRoom, ScoreReport, ScoreReportInput } from './types';

export class DemoMatchBoundaryError extends Error {
  constructor(public readonly capability: MatchCapability) {
    super(`${capability} cannot cross the demo repository boundary`);
    this.name = 'DemoMatchBoundaryError';
  }
}

/** The demo mirrors the server's default report and response windows. */
const DEMO_WINDOW_MS = 10 * 60 * 1000;

function demoReport(matchId: string, kind: ScoreReport['kind'], input: ScoreReportInput, evidenceIds?: string[]): ScoreReport {
  return {
    id: `demo-${kind}-report-${matchId}`,
    kind,
    homeScore: input.homeScore,
    awayScore: input.awayScore,
    tiebreak: input.tiebreak ?? null,
    games: input.games ?? [{ homeScore: input.homeScore, awayScore: input.awayScore }],
    evidenceIds,
    reportedAt: new Date().toISOString(),
  };
}

function demoDeadline() {
  return new Date(Date.now() + DEMO_WINDOW_MS).toISOString();
}

/**
 * Explicit local adapter for design/testing. Evidence never pretends to upload,
 * and the demo opponent never reports, so no local action can confirm a result;
 * production must inject the capability-gated HTTP repository instead.
 */
export const demoMatchRepository: MatchRepository = {
  capabilities: {
    match_detail: 'demo',
    check_in: 'demo',
    signed_evidence_upload: 'unavailable',
    score_report: 'demo',
    final_score_report: 'demo',
  },
  async getMatch(matchId) {
    return getDemoMatchRoom(matchId);
  },
  async checkIn(matchId) {
    const match = getDemoMatchRoom(matchId);
    return { ...match, lifecycle: 'report_required', allowedActions: ['report_score'] };
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
  async reportScore(matchId, input) {
    const match = getDemoMatchRoom(matchId);
    const report = demoReport(match.id, 'initial', input);
    const next: MatchRoom = {
      ...match,
      lifecycle: 'awaiting_opponent_report',
      allowedActions: [],
      resultVerification: {
        ...match.resultVerification,
        phase: 'awaiting_second_report',
        myReport: report,
        reportDeadline: demoDeadline(),
      },
    };
    return { match: next, report };
  },
  async submitFinalScore(matchId, input) {
    const match = getDemoMatchRoom(matchId);
    const report = demoReport(match.id, 'final', input, input.evidenceIds);
    const next: MatchRoom = {
      ...match,
      lifecycle: 'awaiting_opponent_response',
      allowedActions: [],
      resultVerification: { ...match.resultVerification, myFinalReport: report },
    };
    return { match: next, report };
  },
};
