import type {
  BackendCapabilityStatus,
  EvidenceUploadIntent,
  FinalScoreReportInput,
  LocalEvidenceAsset,
  MatchCapabilityMap,
  MatchRepository,
  MatchRoom,
  MatchSummary,
  PreparedEvidenceUpload,
  ScoreReportInput,
  ScoreReportOutcome,
} from '@/features/matches/types';

const apiURL = process.env.EXPO_PUBLIC_API_URL ?? 'http://10.0.2.2:8080';

type AccessTokenProvider = () => string | null | Promise<string | null>;
let accessTokenProvider: AccessTokenProvider | undefined;

/** Attach the auth store without coupling this transport to a state library. */
export function configureAPIClient(options: { getAccessToken: AccessTokenProvider }) {
  accessTokenProvider = options.getAccessToken;
}

export class APIError extends Error {
  constructor(
    public readonly status: number,
    message: string,
    public readonly requestId?: string,
    public readonly code?: string,
  ) {
    super(message);
    this.name = 'APIError';
  }
}

export class BackendCapabilityError extends Error {
  constructor(public readonly capability: MobileAPICapability, public readonly capabilityStatus: BackendCapabilityStatus) {
    super(`${capability} is ${capabilityStatus}; the production API was not called`);
    this.name = 'BackendCapabilityError';
  }
}

/** Every API error is `{ "error": "<code>", "message": "<text>" }`. */
type APIErrorBody = { error?: string; message?: string };

function createRequestId() {
  return globalThis.crypto?.randomUUID?.()
    ?? `mobile-${Date.now().toString(36)}-${Math.random().toString(36).slice(2, 10)}`;
}

export async function request<T>(path: string, init: RequestInit = {}): Promise<T> {
  const headers = new Headers(init.headers);
  headers.set('Accept', 'application/json');
  if (!headers.has('X-Request-ID')) headers.set('X-Request-ID', createRequestId());
  if (init.body !== undefined && !headers.has('Content-Type')) headers.set('Content-Type', 'application/json');
  const accessToken = await accessTokenProvider?.();
  if (accessToken && !headers.has('Authorization')) headers.set('Authorization', `Bearer ${accessToken}`);

  const response = await fetch(`${apiURL}${path}`, { ...init, headers });
  if (!response.ok) {
    let body: APIErrorBody | undefined;
    try {
      body = (await response.json()) as APIErrorBody;
    } catch {
      body = undefined;
    }
    throw new APIError(
      response.status,
      body?.message ?? `Request failed with status ${response.status}`,
      response.headers.get('X-Request-ID') ?? undefined,
      body?.error,
    );
  }
  if (response.status === 204) return undefined as T;
  return response.json() as Promise<T>;
}

export type GameCatalogResponse = {
  data: Array<{
    id: string;
    name: string;
    publisher: string;
    platforms: string[];
    resultMode: string;
    officialIntegration: boolean;
  }>;
};

export const listGames = (signal?: AbortSignal) => request<GameCatalogResponse>('/v1/games', { signal });

export type MobileAPICapability =
  | 'rankings'
  | 'player_discovery'
  | 'public_player_profile'
  | 'public_player_match_history'
  | 'public_player_competition_history'
  | 'my_matches'
  | 'match_detail'
  | 'match_check_in'
  | 'evidence_uploads'
  | 'score_report'
  | 'final_score_report';

/**
 * `demo` means a local adapter may power the screen. `unavailable` means even the
 * demo must stop before that boundary. Only `available` may issue an HTTP call.
 */
export const mobileAPICapabilities: Readonly<Record<MobileAPICapability, BackendCapabilityStatus>> = {
  rankings: 'available',
  player_discovery: 'available',
  public_player_profile: 'available',
  public_player_match_history: 'available',
  public_player_competition_history: 'available',
  my_matches: 'available',
  match_detail: 'available',
  match_check_in: 'available',
  evidence_uploads: 'available',
  score_report: 'available',
  final_score_report: 'available',
};

export const mobileAPIContract = {
  rankings: 'GET /v1/rankings',
  playerSearch: 'GET /v1/players',
  publicPlayerProfile: 'GET /v1/players/{playerId}',
  publicPlayerMatchHistory: 'GET /v1/players/{playerId}/matches',
  publicPlayerCompetitionHistory: 'GET /v1/players/{playerId}/competitions',
  myMatches: 'GET /v1/me/matches',
  matchDetail: 'GET /v1/matches/{matchId}',
  matchCheckIn: 'POST /v1/matches/{matchId}/check-ins',
  evidenceUpload: 'POST /v1/evidence/uploads',
  evidenceUploadComplete: 'POST /v1/evidence/uploads/{id}/complete',
  scoreReport: 'POST /v1/matches/{matchId}/score-reports',
  finalScoreReport: 'POST /v1/matches/{matchId}/score-reports/final',
} as const;

export type CursorPage<T> = {
  data: T[];
  page: { nextCursor: string | null; hasMore: boolean };
};

/** Exact compact row documented for ranking and player-discovery collections. */
export type CompactPlayerRow = {
  rank: number;
  playerId: string;
  handle: string;
  displayName: string;
  avatarUrl?: string;
  countryCode: string;
  rating: number;
  matchesPlayed: number;
  rankMovement: number;
};

export type RankingPage = CursorPage<CompactPlayerRow> & { snapshotAt: string };

type RankingQueryBase = {
  gameId: string;
  limit?: number;
  cursor?: string;
};

export type RankingQuery = RankingQueryBase & (
  | { scope: 'global'; country?: never }
  | { scope: 'country'; country: string }
);

export type PlayerSearchQuery = {
  q: string;
  gameId?: string;
  country?: string;
  limit?: number;
  cursor?: string;
};

/** Planned projection from the fields promised in mobile-api-requirements.md. */
export type PublicPlayerProfile = {
  playerId: string;
  handle: string;
  displayName: string;
  avatarUrl?: string;
  bio?: string;
  countryCode: string;
  ratings: Array<{ gameId: string; rating: number; globalRank?: number; countryRank?: number }>;
  record: { wins: number; draws: number; losses: number };
  publicGameAccounts: Array<{ gameId: string; inGameName: string; verified: boolean }>;
};

/** Planned projection; the endpoint intentionally exposes confirmed public matches only. */
export type PublicPlayerMatchHistory = {
  matchId: string;
  competition: { competitionId: string; name: string };
  opponent: Pick<CompactPlayerRow, 'playerId' | 'handle' | 'displayName' | 'avatarUrl'>;
  score: { player: number; opponent: number };
  outcome: 'win' | 'loss' | 'draw' | 'forfeit';
  playedAt: string;
};

/** Planned projection from the documented placement, format and status fields. */
export type PublicPlayerCompetitionHistory = {
  competitionId: string;
  name: string;
  format: 'single_elimination' | 'double_elimination' | 'round_robin';
  status: 'active' | 'completed';
  placement?: number;
  startedAt: string;
  completedAt?: string;
};

export type MyMatchesQuery = {
  state: 'active' | 'history';
  limit?: number;
  cursor?: string;
};

type WireEvidence = {
  id: string;
  status: string;
  mediaType: string;
  byteSize: number;
  completedAt: string;
};

function assertAvailable(capability: MobileAPICapability) {
  const status = mobileAPICapabilities[capability];
  if (status !== 'available') throw new BackendCapabilityError(capability, status);
}

function encodePathPart(value: string) {
  return encodeURIComponent(value);
}

function boundedLimit(value = 20) {
  return Math.max(1, Math.min(50, value));
}

function addQuery(path: string, query: Record<string, string | number | undefined>) {
  const encoded = Object.entries(query)
    .filter((entry): entry is [string, string | number] => entry[1] !== undefined)
    .map(([key, value]) => `${encodeURIComponent(key)}=${encodeURIComponent(String(value))}`)
    .join('&');
  return encoded ? `${path}?${encoded}` : path;
}

function idempotencyHeaders(idempotencyKey: string) {
  return { 'Idempotency-Key': idempotencyKey };
}

export async function listRankings(input: RankingQuery, signal?: AbortSignal) {
  assertAvailable('rankings');
  return request<RankingPage>(addQuery('/v1/rankings', {
    gameId: input.gameId,
    scope: input.scope,
    country: input.country,
    limit: boundedLimit(input.limit),
    cursor: input.cursor,
  }), { signal });
}

export async function searchPlayers(input: PlayerSearchQuery, signal?: AbortSignal) {
  assertAvailable('player_discovery');
  return request<CursorPage<CompactPlayerRow>>(addQuery('/v1/players', {
    q: input.q,
    gameId: input.gameId,
    country: input.country,
    limit: boundedLimit(input.limit),
    cursor: input.cursor,
  }), { signal });
}

export async function getPublicPlayerProfile(playerId: string, signal?: AbortSignal) {
  assertAvailable('public_player_profile');
  return request<{ data: PublicPlayerProfile }>(`/v1/players/${encodePathPart(playerId)}`, { signal });
}

export async function listPublicPlayerMatches(playerId: string, cursor?: string, signal?: AbortSignal) {
  assertAvailable('public_player_match_history');
  return request<CursorPage<PublicPlayerMatchHistory>>(
    addQuery(`/v1/players/${encodePathPart(playerId)}/matches`, { limit: 20, cursor }),
    { signal },
  );
}

export async function listPublicPlayerCompetitions(playerId: string, cursor?: string, signal?: AbortSignal) {
  assertAvailable('public_player_competition_history');
  return request<CursorPage<PublicPlayerCompetitionHistory>>(
    addQuery(`/v1/players/${encodePathPart(playerId)}/competitions`, { limit: 20, cursor }),
    { signal },
  );
}

export async function listMyMatches(input: MyMatchesQuery, signal?: AbortSignal) {
  assertAvailable('my_matches');
  return request<CursorPage<MatchSummary>>(addQuery('/v1/me/matches', {
    state: input.state,
    limit: boundedLimit(input.limit),
    cursor: input.cursor,
  }), { signal });
}

export async function getMatch(matchId: string, signal?: AbortSignal) {
  assertAvailable('match_detail');
  return request<{ data: MatchRoom }>(`/v1/matches/${encodePathPart(matchId)}`, { signal });
}

export async function checkInToMatch(matchId: string, idempotencyKey: string) {
  assertAvailable('match_check_in');
  return request<{ data: MatchRoom }>(`/v1/matches/${encodePathPart(matchId)}/check-ins`, {
    method: 'POST',
    headers: idempotencyHeaders(idempotencyKey),
    body: JSON.stringify({}),
  });
}

export async function createEvidenceUpload(asset: PreparedEvidenceUpload) {
  assertAvailable('evidence_uploads');
  return request<{ data: EvidenceUploadIntent }>('/v1/evidence/uploads', {
    method: 'POST',
    body: JSON.stringify({ mediaType: asset.contentType, byteSize: asset.byteSize, sha256: asset.sha256 }),
  });
}

export async function uploadEvidenceToProvider(intent: EvidenceUploadIntent, asset: LocalEvidenceAsset) {
  assertAvailable('evidence_uploads');
  const localResponse = await fetch(asset.uri);
  const body = await localResponse.blob();
  const response = await fetch(intent.uploadUrl, { method: 'PUT', headers: intent.requiredHeaders, body });
  if (!response.ok) throw new APIError(response.status, 'The result screenshot could not be uploaded');
}

export async function completeEvidenceUpload(evidenceId: string) {
  assertAvailable('evidence_uploads');
  return request<{ data: WireEvidence }>(`/v1/evidence/uploads/${encodePathPart(evidenceId)}/complete`, {
    method: 'POST',
    body: JSON.stringify({}),
  });
}

/**
 * Reports the caller's entry's score blind: score only, no screenshot. The
 * response is the caller's own room and report; it never contains the
 * opponent's claim.
 */
export async function submitScoreReport(matchId: string, input: ScoreReportInput) {
  assertAvailable('score_report');
  const { idempotencyKey, ...report } = input;
  return request<{ data: ScoreReportOutcome }>(`/v1/matches/${encodePathPart(matchId)}/score-reports`, {
    method: 'POST',
    headers: idempotencyHeaders(idempotencyKey),
    body: JSON.stringify(report),
  });
}

/** Sends the entry's one final score after a mismatch, with one to three ready screenshots. */
export async function submitFinalScoreReport(matchId: string, input: FinalScoreReportInput) {
  assertAvailable('final_score_report');
  const { idempotencyKey, ...report } = input;
  return request<{ data: ScoreReportOutcome }>(`/v1/matches/${encodePathPart(matchId)}/score-reports/final`, {
    method: 'POST',
    headers: idempotencyHeaders(idempotencyKey),
    body: JSON.stringify(report),
  });
}

export const httpMatchRepository: MatchRepository = {
  capabilities: {
    match_detail: mobileAPICapabilities.match_detail,
    check_in: mobileAPICapabilities.match_check_in,
    signed_evidence_upload: mobileAPICapabilities.evidence_uploads,
    score_report: mobileAPICapabilities.score_report,
    final_score_report: mobileAPICapabilities.final_score_report,
  } satisfies MatchCapabilityMap,
  async getMatch(matchId, signal) {
    return (await getMatch(matchId, signal)).data;
  },
  async checkIn(matchId, idempotencyKey) {
    return (await checkInToMatch(matchId, idempotencyKey)).data;
  },
  async createEvidenceUploadIntent(asset) {
    return (await createEvidenceUpload(asset)).data;
  },
  uploadEvidence: uploadEvidenceToProvider,
  async completeEvidenceUpload(evidenceId) {
    const evidence = (await completeEvidenceUpload(evidenceId)).data;
    return { id: evidence.id, fileName: 'Result screenshot', contentType: evidence.mediaType, uploadedAt: evidence.completedAt };
  },
  async reportScore(matchId, input) {
    return (await submitScoreReport(matchId, input)).data;
  },
  async submitFinalScore(matchId, input) {
    return (await submitFinalScoreReport(matchId, input)).data;
  },
};
