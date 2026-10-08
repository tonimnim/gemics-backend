import { useQuery } from '@tanstack/react-query'
import { useCan } from '@/stores/auth-store'
import { api } from '@/lib/api'

/** A row of GET /v1/admin/fx-rates: units of the currency one dollar buys. */
export type FxRate = {
  currency: string
  name: string
  minorUnit: number
  unitsPerUsd: string | null
  rateDate: string | null
  source: string | null
  fetchedAt: string | null
  stale: boolean
}

export function useFxRates() {
  const allowed = useCan('overview.view')
  return useQuery({
    queryKey: ['fx-rates'],
    queryFn: () => api<{ data: FxRate[] }>('/v1/admin/fx-rates'),
    select: (response) => response.data,
    enabled: allowed,
    staleTime: 10 * 60 * 1000,
  })
}

/** The newest rate for a currency, as units per dollar. */
export function rateFor(rates: FxRate[] | undefined, currency: string) {
  if (currency === 'USD') return '1'
  return rates?.find((rate) => rate.currency === currency)?.unitsPerUsd ?? null
}

export const financePeriods = [7, 30, 90, 365] as const
export type FinancePeriod = (typeof financePeriods)[number]

/** GET /v1/admin/finance */
export type FinanceReport = {
  days: number
  from: string
  to: string
  totals: {
    collectedUsdMinor: number
    refundedUsdMinor: number
    netUsdMinor: number
    payments: number
    refunds: number
    unconverted: number
    prizesUsdMinor: number
    upcomingPrizesUsdMinor: number
  }
  byCurrency: {
    currency: string
    minorUnit: number
    collectedMinor: number
    refundedMinor: number
    netMinor: number
    collectedUsdMinor: number
    refundedUsdMinor: number
    payments: number
    refunds: number
  }[]
  daily: { date: string; collectedUsdMinor: number; refundedUsdMinor: number }[]
  prizes: {
    currency: string
    minorUnit: number
    prizesMinor: number
    upcomingMinor: number
    prizesUsdMinor: number | null
    upcomingUsdMinor: number | null
    competitions: number
  }[]
  recent: {
    kind: 'payment' | 'refund'
    id: string
    at: string
    player: string
    competition: string
    amountMinor: number
    currency: string
    usdMinor: number | null
  }[]
  rates: FxRate[]
}

export function useFinance(days: FinancePeriod) {
  return useQuery({
    queryKey: ['finance', days],
    queryFn: () =>
      api<{ data: FinanceReport }>('/v1/admin/finance', { query: { days } }),
    select: (response) => response.data,
    refetchInterval: 60_000,
  })
}
