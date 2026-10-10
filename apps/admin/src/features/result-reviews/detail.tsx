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
  submitted,
  rejected,
  screenshot,
  strikes,
}: {
  reviewId: string
  side: 'Home' | 'Away'
  name: string
  submitted: StaffReport | null
  rejected: boolean
  screenshot: StaffReport | null
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
        <div className='text-sm'>
          {submitted &&
          submitted.homeScore != null &&
          submitted.awayScore != null ? (
            <>
              <div className='font-mono text-xs text-muted-foreground uppercase'>
                Submitted the result
              </div>
              <div className='text-2xl font-black'>
                {scoreText({
                  homeScore: submitted.homeScore,
                  awayScore: submitted.awayScore,
                  tiebreak: submitted.tiebreak,
                })}
              </div>
              <div className='text-xs text-muted-foreground'>
                {dateTime(submitted.reportedAt)}
              </div>
            </>
          ) : (
            <div className='font-mono text-xs text-muted-foreground uppercase'>
              {rejected ? 'Rejected the result' : 'Did not answer'}
            </div>
          )}
        </div>
        {screenshot && screenshot.evidence.length > 0 ? (
          <div className='flex flex-wrap gap-3'>
            {screenshot.evidence.map((item) => (
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
          <p className='text-sm text-muted-foreground'>No screenshot.</p>
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
  // accept stands for accepting the submitted result, whichever side sent it.
  const [decision, setDecision] = useState<
    'accept' | Exclude<ReviewDecision, 'accept_home' | 'accept_away'>
  >('accept')
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
          decision:
            decision === 'accept' ? `accept_${submittedSide}` : decision,
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

  const submittedSide: 'home' | 'away' = review?.reports.away.initial
    ? 'away'
    : 'home'
  const otherSide = submittedSide === 'home' ? 'away' : 'home'
  const submitted = review?.reports[submittedSide].initial ?? null
  const rejectedBy = review?.verification.rejectedBy ?? null
  // Only the player a decision proves wrong can be struck: the submitter or
  // the player who rejected the result.
  const players = review
    ? [
        submitted && {
          userId: submitted.reportedBy.userId,
          name: `${submitted.reportedBy.displayName} (submitted the result)`,
        },
        rejectedBy && {
          userId: rejectedBy,
          name: `${review.participants[otherSide].displayName} (rejected it)`,
        },
      ].filter((player) => !!player)
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
              submitted={review.reports.home.initial}
              rejected={submittedSide === 'away' && !!rejectedBy}
              screenshot={review.reports.home.final}
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
              submitted={review.reports.away.initial}
              rejected={submittedSide === 'home' && !!rejectedBy}
              screenshot={review.reports.away.final}
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
                      setDecision(value as typeof decision)
                    }
                    className='grid gap-2 sm:grid-cols-2'
                  >
                    {(
                      [
                        [
                          'accept',
                          submitted &&
                          submitted.homeScore != null &&
                          submitted.awayScore != null
                            ? `Accept the submitted result (${scoreText({
                                homeScore: submitted.homeScore,
                                awayScore: submitted.awayScore,
                                tiebreak: submitted.tiebreak,
                              })})`
                            : 'Accept the submitted result',
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
                    <Label>Conduct strike</Label>
                    {players.map((player) => (
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
