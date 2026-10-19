import type { CompetitionFormat } from './api'

/** rules.matchFormat: how many games each match is. */
export type MatchFormat = { bestOf: BestOf; rounds?: Record<string, BestOf> }
export type BestOf = 1 | 3 | 5
export const bestOfChoices: BestOf[] = [1, 3, 5]

/**
 * The rounds a competition can override, latest first. Rounds are named back
 * from the final, so the format holds whatever the final bracket size.
 */
export function formatRounds(
  format: CompetitionFormat,
  maxEntries: number
): { key: string; label: string }[] {
  if (format === 'round_robin') return []
  if (format === 'double_elimination') {
    return [
      { key: 'final', label: 'Grand final' },
      { key: 'winners_final', label: 'Winners final' },
      { key: 'losers_final', label: 'Losers final' },
    ]
  }
  const rounds = Math.max(1, Math.ceil(Math.log2(Math.max(2, maxEntries))))
  const named = [
    { key: 'final', label: 'Final' },
    { key: 'semifinal', label: 'Semifinal' },
    { key: 'quarterfinal', label: 'Quarterfinal' },
  ]
  for (let size = 16; named.length < rounds; size *= 2) {
    named.push({ key: `round_of_${size}`, label: `Round of ${size}` })
  }
  const list = named.slice(0, rounds)
  if (rounds >= 2)
    list.splice(1, 0, { key: 'third_place', label: 'Third place' })
  return list
}

/** The format to save: overrides equal to the default are dropped. */
export function cleanMatchFormat(
  format: MatchFormat,
  competitionFormat: CompetitionFormat,
  maxEntries: number
): MatchFormat {
  const allowed = new Set(
    formatRounds(competitionFormat, maxEntries).map((round) => round.key)
  )
  const rounds = Object.fromEntries(
    Object.entries(format.rounds ?? {}).filter(
      ([key, value]) => allowed.has(key) && value !== format.bestOf
    )
  )
  return Object.keys(rounds).length
    ? { bestOf: format.bestOf, rounds }
    : { bestOf: format.bestOf }
}

export function readMatchFormat(rules: unknown): MatchFormat {
  const raw = (rules as { matchFormat?: Partial<MatchFormat> } | undefined)
    ?.matchFormat
  const valid = (value: unknown): value is BestOf =>
    bestOfChoices.includes(value as BestOf)
  const rounds = Object.fromEntries(
    Object.entries(raw?.rounds ?? {}).filter(([, value]) => valid(value))
  ) as Record<string, BestOf>
  const bestOf = raw?.bestOf
  return { bestOf: valid(bestOf) ? bestOf : 1, rounds }
}

const roundLabel = (key: string) =>
  ({
    final: 'final',
    semifinal: 'semifinal',
    quarterfinal: 'quarterfinal',
    third_place: 'third place',
    winners_final: 'winners final',
    losers_final: 'losers final',
  })[key] ?? key.replace(/_/g, ' ')

/** Earlier rounds first: round of 32, …, semifinal, third place, final. */
const playOrder = (key: string) => {
  const order = [
    'quarterfinal',
    'winners_final',
    'semifinal',
    'losers_final',
    'third_place',
    'final',
  ]
  const size = Number(key.replace('round_of_', ''))
  return key.startsWith('round_of_') ? -size : order.indexOf(key)
}

/** "Best of 1 · semifinal Bo3 · final Bo5" */
export function describeMatchFormat(rules: unknown): string {
  const format = readMatchFormat(rules)
  const overrides = Object.entries(format.rounds ?? {})
    .sort(([a], [b]) => playOrder(a) - playOrder(b))
    .map(([key, value]) => `${roundLabel(key)} Bo${value}`)
  return [`Best of ${format.bestOf}`, ...overrides].join(' · ')
}
