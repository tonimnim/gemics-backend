import { useMutation, useQueryClient } from '@tanstack/react-query'
import {
  AlertTriangle,
  CheckCircle2,
  Loader2,
  RotateCw,
  ScanText,
} from 'lucide-react'
import { toast } from 'sonner'
import { api } from '@/lib/api'
import { humanize } from '@/lib/format'
import { cn } from '@/lib/utils'
import type {
  ReviewDetail,
  ScreenshotCheck,
  ScreenshotReading,
  ScreenshotScore,
} from './api'

const flagLabels: Record<string, string> = {
  not_read: "Couldn't be read",
  not_result_screen: 'Not the Full Time screen',
  low_confidence: 'Low confidence',
  teams_unknown: "Players' team names not known yet",
  not_full_time: 'Not the full-time screen',
  full_time_unknown: '"Full Time" not read',
  reused_stats: 'Same stats table as another match',
  similar_image: 'Looks like a screenshot from another match',
  stats_incomplete: 'Some stats unread',
  possession_not_100: "Possession doesn't add up",
  on_target_exceeds_shots: 'More on target than shots',
  goals_exceed_shots_on_target: 'More goals than shots on target',
  goals_and_saves_exceed_shots_on_target:
    'Goals + saves exceed shots on target',
  successful_exceeds_passes: 'More successful passes than passes',
}

/** Flags that inform staff without counting against the screenshot. */
const hintFlags = new Set([
  'similar_image',
  'teams_unknown',
  'stats_incomplete',
  'goals_and_saves_exceed_shots_on_target',
])

const statLabels: Record<string, string> = {
  shotsOnTarget: 'shots on target',
  cornerKicks: 'corner kicks',
  freeKicks: 'free kicks',
  successfulPasses: 'successful passes',
}

const fieldLabel = (field: string) => statLabels[field] ?? humanize(field)

function scoreLine(score: ScreenshotScore) {
  const penalties =
    score.homePenalties != null && score.awayPenalties != null
      ? ` (${score.homePenalties}-${score.awayPenalties} pens)`
      : ''
  return `${score.homeScore}-${score.awayScore}${penalties}`
}

/** The reader's verdict across every screenshot of the review. */
export function ScreenshotVerdict({
  check,
  review,
}: {
  check: ScreenshotCheck
  review: ReviewDetail
}) {
  const homeName = review.participants.home.displayName
  const awayName = review.participants.away.displayName
  const text: Record<ScreenshotCheck['verdict'], string> = {
    pending: 'Reading the screenshots…',
    supports: check.score
      ? `The screenshots show ${homeName} ${scoreLine(check.score)} ${awayName}, matching the ${
          check.supportedSide ?? 'home'
        } claim.`
      : 'The screenshots match one claim.',
    neither: check.score
      ? `The screenshots show ${homeName} ${scoreLine(check.score)} ${awayName}, matching neither claim.`
      : 'The screenshots match neither claim.',
    conflicting: `The screenshots disagree on ${check.differences.map(fieldLabel).join(', ')}.`,
    unreadable: "The screenshots couldn't be read.",
    inconclusive:
      "The screenshots agree, but their team names don't match the players yet.",
  }
  const tone =
    check.verdict === 'supports'
      ? 'border-[#4ccb74]/40 bg-[#4ccb74]/10 text-[#23824a]'
      : check.verdict === 'conflicting' || check.verdict === 'neither'
        ? 'border-[#ff7448]/40 bg-[#ff7448]/10 text-[#c4421d]'
        : 'border-border bg-muted text-muted-foreground'
  const Icon =
    check.verdict === 'pending'
      ? Loader2
      : check.verdict === 'supports'
        ? CheckCircle2
        : check.verdict === 'conflicting' || check.verdict === 'neither'
          ? AlertTriangle
          : ScanText
  return (
    <div className={cn('flex items-start gap-3 rounded-2xl border p-4', tone)}>
      <Icon
        className={cn(
          'mt-0.5 size-5 shrink-0',
          check.verdict === 'pending' && 'animate-spin'
        )}
      />
      <div className='grid gap-0.5'>
        <div className='font-medium'>{text[check.verdict]}</div>
        {/* Elsewhere the headline already says why; here it explains what
            keeps a supported claim from being settled automatically. */}
        {check.verdict === 'supports' && check.reasons.length > 0 && (
          <div className='text-sm opacity-80'>{check.reasons.join(' ')}</div>
        )}
        {check.autoDecide && (
          <div className='text-sm opacity-80'>
            Both players' screenshots agree, so this will be settled
            automatically.
          </div>
        )}
      </div>
    </div>
  )
}

/** What the reader read from one screenshot, shown under the image. */
export function ScreenshotReadingNote({
  reviewId,
  evidenceId,
  reading,
}: {
  reviewId: string
  evidenceId: string
  reading: ScreenshotReading
}) {
  const queryClient = useQueryClient()
  const retry = useMutation({
    mutationFn: () =>
      api(`/v1/admin/screenshot-readings/${evidenceId}/retry`, {
        method: 'POST',
      }),
    onSuccess: () => {
      toast.success('Reading the screenshot again.')
      queryClient.invalidateQueries({ queryKey: ['result-review', reviewId] })
    },
  })
  return (
    <div className='w-40 space-y-1 text-xs'>
      {reading.status === 'queued' ? (
        <div className='flex items-center gap-1 text-muted-foreground'>
          <Loader2 className='size-3 animate-spin' /> Reading…
        </div>
      ) : reading.left && reading.right ? (
        <div className='font-medium'>
          {reading.left.team} {reading.left.score}-{reading.right.score}{' '}
          {reading.right.team}
          {reading.penalties &&
            ` (${reading.penalties.left}-${reading.penalties.right} pens)`}
          {reading.confidence != null && (
            <span className='ms-1 font-normal text-muted-foreground'>
              {Math.round(reading.confidence * 100)}%
            </span>
          )}
        </div>
      ) : (
        <div className='text-muted-foreground'>
          {reading.status === 'failed'
            ? "Couldn't be read"
            : 'Not the Full Time screen'}
        </div>
      )}
      {reading.flags
        .filter((flag) => flag !== 'not_read' && flag !== 'not_result_screen')
        .map((flag) => (
          <div
            key={flag}
            className={
              hintFlags.has(flag) ? 'text-muted-foreground' : 'text-[#c4421d]'
            }
          >
            {flagLabels[flag] ?? humanize(flag)}
          </div>
        ))}
      {reading.status !== 'queued' && (
        <button
          type='button'
          onClick={() => retry.mutate()}
          disabled={retry.isPending}
          className='inline-flex items-center gap-1 text-muted-foreground hover:text-foreground'
        >
          <RotateCw
            className={cn('size-3', retry.isPending && 'animate-spin')}
          />
          Read again
        </button>
      )}
    </div>
  )
}
