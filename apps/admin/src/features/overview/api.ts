import { useQuery } from '@tanstack/react-query'
import { useCan } from '@/stores/auth-store'
import { api } from '@/lib/api'

/** GET /v1/admin/overview */
export type Overview = {
  players: { total: number; newLast7Days: number }
  competitions: { registrationOpen: number; running: number; draft: number }
  queues: {
    resultReviews: number
    accountVerifications: number
  }
  /** Daily counts for the last 14 days, oldest first. */
  playersByDay: { date: string; count: number }[]
  registrationsByDay: { date: string; count: number }[]
  competitionsByStatus: Record<string, number>
  /** Money is null for staff without finance.view. */
  finance: {
    paymentReviews: number
    refunds: number
    succeededLast30Days: number
    collectedMinorLast30Days: number
    /** Payments still waiting for an exchange rate, not in the total. */
    unconvertedLast30Days: number
    currency: string
  } | null
}

export function useOverview() {
  const allowed = useCan('overview.view')
  return useQuery({
    queryKey: ['overview'],
    queryFn: () => api<Overview>('/v1/admin/overview'),
    enabled: allowed,
    refetchInterval: 30_000,
  })
}
