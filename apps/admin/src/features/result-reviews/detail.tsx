import { useState } from 'react'
import { useMutation, useQuery, useQueryClient } from '@tanstack/react-query'
import { Link } from '@tanstack/react-router'
import { ArrowLeft, Loader2 } from 'lucide-react'
import { toast } from 'sonner'
import { api, errorMessage } from '@/lib/api'
import { dateTime, humanize } from '@/lib/format'
import { Alert, AlertDescription } from '@/components/ui/alert'
import { Button } from '@/components/ui/button'
import { Card, CardContent, CardHeader, CardTitle } from '@/components/ui/card'
import { Checkbox } from '@/components/ui/checkbox'
import { Input } from '@/components/ui/input'
import { Label } from '@/components/ui/label'
import { RadioGroup, RadioGroupItem } from '@/components/ui/radio-group'
import { Textarea } from '@/components/ui/textarea'
import { EvidenceImage } from '@/components/evidence-image'
import { Page } from '@/components/page'
import { StatusBadge } from '@/components/status-badge'
import {
  scoreText,
  type ReviewDecision,
  type ReviewDetail,
  type StaffReport,
} from './api'
import { ScreenshotReadingNote, ScreenshotVerdict } from './screenshot-reading'

function ReportCard({
  reviewId,
  side,
  name,
  initial,
  final,
  strikes,
}: {
  reviewId: string
  side: 'Home' | 'Away'
  name: string
  initial: StaffReport | null
  final: StaffReport | null
  strikes: number
}) {
  return (
    <Card>
      <CardHeader>
        <CardTitle className='flex items-center justify-between text-base'>
          <span>
            <span className='font-mono text-xs text-muted-foreground uppercase'>
              {side}
            </span>{' '}
            {name}
          </span>
          {strikes > 0 && (
            <span className='text-xs font-normal text-[#ff7448]'>
              {strikes} active strike{strikes > 1 ? 's' : ''}
            </span>
          )}
        </CardTitle>
      </CardHeader>
      <CardContent className='space-y-4'>
        <div className='grid grid-cols-2 gap-3 text-sm'>
          <div>
            <div className='font-mono text-xs text-muted-foreground uppercase'>
              First claim
            </div>
            <div className='text-2xl font-black'>
              {initial ? scoreText(initial) : '—'}
            </div>
            {initial && (
              <div className='text-xs text-muted-foreground'>
                {dateTime(initial.reportedAt)}
              </div>
            )}
          </div>
          <div>
            <div className='font-mono text-xs text-muted-foreground uppercase'>
              Final claim
            </div>
            <div className='text-2xl font-black'>
              {final ? scoreText(final) : '—'}
            </div>
            {final && (
              <div className='text-xs text-muted-foreground'>
                {dateTime(final.reportedAt)}
              </div>
            )}
          </div>
        </div>
        {final && final.evidence.length > 0 ? (
          <div className='flex flex-wrap gap-3'>
            {final.evidence.map((item) => (
              <div key={item.id} className='space-y-2'>
                <EvidenceImage id={item.id} ready={item.ready} />
                {item.reading && (
                  <ScreenshotReadingNote
                    reviewId={reviewId}
                    evidenceId={item.id}
                    reading={item.reading}
                  />
                )}
              </div>
            ))}
          </div>
        ) : (
          <p className='text-sm text-muted-foreground'>No screenshots.</p>
        )}
      </CardContent>
    </Card>
  )
}

export function ResultReviewDetailPage({ id }: { id: string }) {
  const queryClient = useQueryClient()
  const { data, isLoading, error } = useQuery({
    queryKey: ['result-review', id],
    queryFn: () =>
      api<{ data: ReviewDetail }>(`/v1/admin/result-reviews/${id}`),
  })
  const review = data?.data
  const [decision, setDecision] = useState<ReviewDecision>('accept_home')
  const [score, setScore] = useState({ home: '0', away: '0' })
  const [strikeUserIds, setStrikeUserIds] = useState<string[]>([])
  const [note, setNote] = useState('')

  const decide = useMutation({
    mutationFn: () =>
      api<{ data: ReviewDetail }>(`/v1/admin/result-reviews/${id}/decisions`, {
        method: 'POST',
        idempotent: true,
        body: {
          expectedVersion: review!.version,
          decision,
          correctedScore:
            decision === 'corrected_score'
              ? { homeScore: Number(score.home), awayScore: Number(score.away) }
              : null,
          strikeUserIds,
          note: note.trim(),
        },
      }),
    onSuccess: (result) => {
      queryClient.setQueryData(['result-review', id], result)
      queryClient.invalidateQueries({ queryKey: ['result-reviews'] })
      queryClient.invalidateQueries({ queryKey: ['overview'] })
      toast.success('Decision recorded. Progression continues automatically.')
    },
  })

  const reporters = review
    ? [
        {
          userId: review.participants.home.captainUserId,
          name: review.participants.home.displayName,
        },
        {
          userId: review.participants.away.captainUserId,
          name: review.participants.away.displayName,
        },
      ]
    : []

  return (
    <Page
      title={review ? review.match.competitionName : 'Result review'}
      description={
        review &&
        `${humanize(review.match.bracket)} · Round ${review.match.roundNumber} · Match ${review.match.matchNumber} · Best of ${review.match.bestOf}`
      }
      permission='result_review.manage'
      actions={
        <Button variant='outline' asChild>
          <Link to='/result-reviews'>
            <ArrowLeft /> Back to queue
          </Link>
        </Button>
      }
    >
      {isLoading && <Loader2 className='animate-spin' />}
      {error && (
        <Alert variant='destructive'>
          <AlertDescription>{errorMessage(error)}</AlertDescription>
        </Alert>
      )}
      {review && (
        <div className='space-y-6'>
          <div className='flex flex-wrap items-center gap-3 text-sm'>
            <StatusBadge status={review.status} />
            <span className='text-muted-foreground'>
              {humanize(review.reason)}
            </span>
            <span className='text-muted-foreground'>
              Queued {dateTime(review.queuedAt)}
            </span>
          </div>
          {review.screenshotCheck && (
            <ScreenshotVerdict check={review.screenshotCheck} review={review} />
          )}
          <div className='grid gap-4 lg:grid-cols-2'>
            <ReportCard
              reviewId={review.id}
              side='Home'
              name={review.participants.home.displayName}
              initial={review.reports.home.initial}
              final={review.reports.home.final}
              strikes={
                review.activeStrikeCounts[
                  review.participants.home.captainUserId
                ] ?? 0
              }
            />
            <ReportCard
              reviewId={review.id}
              side='Away'
              name={review.participants.away.displayName}
              initial={review.reports.away.initial}
              final={review.reports.away.final}
              strikes={
                review.activeStrikeCounts[
                  review.participants.away.captainUserId
                ] ?? 0
              }
            />
          </div>

          {review.decision ? (
            <Card>
              <CardHeader>
                <CardTitle className='text-base'>
                  Decision: {humanize(review.decision.decision)}
                </CardTitle>
              </CardHeader>
              <CardContent className='space-y-1 text-sm'>
                {review.decision.correctedScore && (
                  <div>Score: {scoreText(review.decision.correctedScore)}</div>
                )}
                <div className='text-muted-foreground'>
                  {review.decision.note}
                </div>
                <div className='text-xs text-muted-foreground'>
                  {review.decision.deciderKind === 'system'
                    ? 'Decided automatically'
                    : 'Decided by staff'}{' '}
                  · {dateTime(review.decision.decidedAt)}
                </div>
              </CardContent>
            </Card>
          ) : review.status === 'queued' ? (
            <Card>
              <CardHeader>
                <CardTitle className='text-base'>Decide</CardTitle>
              </CardHeader>
              <CardContent>
                <form
                  className='grid gap-5'
                  onSubmit={(event) => {
                    event.preventDefault()
                    decide.mutate()
                  }}
                >
                  <RadioGroup
                    value={decision}
                    onValueChange={(value) =>
                      setDecision(value as ReviewDecision)
                    }
                    className='grid gap-2 sm:grid-cols-2'
                  >
                    {(
                      [
                        [
                          'accept_home',
                          `Accept ${review.participants.home.displayName}'s claim`,
                        ],
                        [
                          'accept_away',
                          `Accept ${review.participants.away.displayName}'s claim`,
                        ],
                        ['corrected_score', 'Enter the correct score'],
                        ['remove_both', 'Remove both players'],
                      ] as const
                    ).map(([value, label]) => (
                      <Label
                        key={value}
                        className='flex items-center gap-2 rounded-md border p-3 font-normal'
                      >
                        <RadioGroupItem value={value} /> {label}
                      </Label>
                    ))}
                  </RadioGroup>
                  {decision === 'corrected_score' && (
                    <div className='flex items-end gap-3'>
                      <div className='grid gap-1'>
                        <Label htmlFor='home'>Home</Label>
                        <Input
                          id='home'
                          type='number'
                          min={0}
                          max={99}
                          className='w-24'
                          value={score.home}
                          onChange={(e) =>
                            setScore({ ...score, home: e.target.value })
                          }
                        />
                      </div>
                      <span className='pb-2'>–</span>
                      <div className='grid gap-1'>
                        <Label htmlFor='away'>Away</Label>
                        <Input
                          id='away'
                          type='number'
                          min={0}
                          max={99}
                          className='w-24'
                          value={score.away}
                          onChange={(e) =>
                            setScore({ ...score, away: e.target.value })
                          }
                        />
                      </div>
                    </div>
                  )}
                  <div className='grid gap-2'>
                    <Label>Conduct strike for a false report</Label>
                    {reporters.map((player) => (
                      <Label
                        key={player.userId}
                        className='flex items-center gap-2 font-normal'
                      >
                        <Checkbox
                          checked={strikeUserIds.includes(player.userId)}
                          onCheckedChange={(checked) =>
                            setStrikeUserIds((ids) =>
                              checked
                                ? [...ids, player.userId]
                                : ids.filter((value) => value !== player.userId)
                            )
                          }
                        />
                        {player.name}
                      </Label>
                    ))}
                  </div>
                  <div className='grid gap-2'>
                    <Label htmlFor='note'>
                      Note (shown in the audit trail)
                    </Label>
                    <Textarea
                      id='note'
                      value={note}
                      onChange={(e) => setNote(e.target.value)}
                      required
                      maxLength={1000}
                    />
                  </div>
                  <div>
                    <Button disabled={decide.isPending || !note.trim()}>
                      {decide.isPending && <Loader2 className='animate-spin' />}
                      Record decision
                    </Button>
                  </div>
                </form>
              </CardContent>
            </Card>
          ) : null}
        </div>
      )}
    </Page>
  )
}
