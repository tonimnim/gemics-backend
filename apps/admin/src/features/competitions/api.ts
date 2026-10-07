import { api } from '@/lib/api'

export const competitionStatuses = [
  'draft',
  'published',
  'registration_open',
  'check_in',
  'running',
  'completed',
  'cancelled',
] as const
export type CompetitionStatus = (typeof competitionStatuses)[number]
export type CompetitionFormat =
  'single_elimination' | 'double_elimination' | 'round_robin'

/** OrganizerCompetition, as GET /v1/organizations/{orgId}/competitions returns it. */
export type Competition = {
  id: string
  gameId: string
  gameName?: string
  name: string
  slug: string
  description?: string
  format: CompetitionFormat
  status: CompetitionStatus
  maxEntries: number
  entryCount: number
  entryFeeMinor?: number
  currency?: string
  prizeAmountMinor?: number
  prizeFunding?: 'none' | 'organizer' | 'sponsor'
  registrationOpensAt?: string
  registrationClosesAt?: string
  startsAt?: string
  allowedTransitions: CompetitionStatus[]
  matchCount?: number
  rules?: Record<string, unknown>
}

export type CompetitionInput = {
  name: string
  description: string
  gameId: string
  format: CompetitionFormat
  maxEntries: number
  entryFeeMinor: number
  currency: 'KES'
  prizeAmountMinor: number
  prizeFunding: 'none' | 'organizer' | 'sponsor'
  registrationOpensAt: string
  registrationClosesAt: string
  startsAt: string
  rules: Record<string, unknown>
}

export type DrawRequest = {
  seedingPolicy: 'seeded' | 'rating' | 'random' | 'registration_order'
  expectedStatus: 'check_in'
  config: { bestOf: number; thirdPlace?: boolean; groupCount?: number }
}

export const competitionsPath = (orgId: string) =>
  `/v1/organizations/${orgId}/competitions`

export const createCompetition = (orgId: string, input: CompetitionInput) =>
  api<{ data: Competition }>(competitionsPath(orgId), {
    method: 'POST',
    body: input,
  })

export const transitionCompetition = (
  orgId: string,
  id: string,
  status: CompetitionStatus,
  reason: string
) =>
  api<{ data: Competition }>(`${competitionsPath(orgId)}/${id}/transitions`, {
    method: 'POST',
    body: reason ? { status, reason } : { status },
  })

export const drawCompetition = (orgId: string, id: string, draw: DrawRequest) =>
  api(`${competitionsPath(orgId)}/${id}/draws`, { method: 'POST', body: draw })
