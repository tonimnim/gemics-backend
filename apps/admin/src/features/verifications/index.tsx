import { useMemo, useState } from 'react'
import { useMutation, useQueryClient } from '@tanstack/react-query'
import { BadgeCheck, Copy, Loader2, Search } from 'lucide-react'
import { toast } from 'sonner'
import { api, errorMessage } from '@/lib/api'
import { countryFlag, countryName } from '@/lib/countries'
import { ago, dateTime, initials } from '@/lib/format'
import { usePagedList } from '@/lib/paged'
import { Button } from '@/components/ui/button'
import {
  Sheet,
  SheetContent,
  SheetHeader,
  SheetTitle,
} from '@/components/ui/sheet'
import {
  DecisionDialog,
  type DecisionOption,
} from '@/components/decision-dialog'
import { EvidenceImage } from '@/components/evidence-image'
import { LoadMore } from '@/components/list-state'
import { Page } from '@/components/page'
import { StatusBadge } from '@/components/status-badge'

/** A row of GET /v1/admin/game-account-verifications. */
type Verification = {
  id: string
  gameAccountId: string
  userId: string
  method: string
  status: string
  playerNote: string
  decisionReason: string
  evidenceIds: string[]
  requestedAt: string
  reviewedAt: string | null
  player: {
    username: string | null
    displayName: string
    countryCode: string
    konamiId: string | null
    inGameName: string
    previousRejections: number
  }
}

type Decision = 'approve' | 'reject'

/** Requests still waiting on a reviewer. */
function isOpen(status: string) {
  return status === 'requested' || status === 'under_review'
}

const options: Record<Decision, DecisionOption> = {
  approve: { label: 'Approve verification', noteLabel: 'Reason' },
  reject: {
    label: 'Reject verification',
    destructive: true,
    noteLabel: 'Reason shown to the player',
    noteRequired: true,
  },
}

const nameOf = (item: Verification) =>
  item.player.username ?? item.player.displayName

export function VerificationsPage() {
  const queryClient = useQueryClient()
  const [search, setSearch] = useState('')
  const [selectedId, setSelectedId] = useState<string | null>(null)
  const [deciding, setDeciding] = useState<{
    item: Verification
    decision: Decision
  } | null>(null)
  const list = usePagedList<Verification>(
    ['verifications'],
    '/v1/admin/game-account-verifications',
    { status: 'all' }
  )
  const visible = useMemo(() => {
    const term = search.trim().toLowerCase()
    if (!term) return list.items
    return list.items.filter((item) =>
      [nameOf(item), item.player.displayName, item.player.konamiId ?? '']
        .join(' ')
        .toLowerCase()
        .includes(term)
    )
  }, [list.items, search])
  const waiting = list.items.filter((item) => isOpen(item.status)).length
  const selected = list.items.find((item) => item.id === selectedId) ?? null

  const decide = useMutation({
    mutationFn: (reason: string) =>
      api(
        `/v1/admin/game-account-verifications/${deciding!.item.id}/decisions`,
        {
          method: 'POST',
          idempotent: true,
          body: { decision: deciding!.decision, reason },
        }
      ),
    onSuccess: () => {
      toast.success(
        deciding?.decision === 'approve'
          ? 'Account verified.'
          : 'Verification rejected.'
      )
      setDeciding(null)
      queryClient.invalidateQueries({ queryKey: ['verifications'] })
      queryClient.invalidateQueries({ queryKey: ['overview'] })
    },
  })

  return (
    <Page
      title='Account verifications'
      permission='game_account_verification.manage'
    >
      <section className='overflow-hidden rounded-2xl bg-card'>
        <div className='flex flex-wrap items-center justify-between gap-3 border-b px-5 py-4'>
          <div className='flex items-center gap-2'>
            <h2 className='font-semibold'>All requests</h2>
            <span className='rounded-full bg-muted px-2 py-0.5 text-xs font-medium text-muted-foreground'>
              {list.items.length}
              {list.hasNextPage ? '+' : ''}
            </span>
            {waiting > 0 && (
              <span className='rounded-full bg-[#ff7448]/15 px-2 py-0.5 text-xs font-medium text-[#c4421d]'>
                {waiting} waiting
              </span>
            )}
          </div>
          <label className='relative block w-full sm:w-72'>
            <Search className='pointer-events-none absolute start-3.5 top-1/2 size-4 -translate-y-1/2 text-muted-foreground' />
            <input
              value={search}
              onChange={(event) => setSearch(event.target.value)}
              placeholder='Search player or Konami ID'
              className='h-10 w-full rounded-xl border border-transparent bg-muted ps-10 pe-4 text-sm outline-none focus:border-primary focus:bg-card focus:ring-4 focus:ring-primary/15'
            />
          </label>
        </div>

        <div className='hidden grid-cols-[minmax(0,2fr)_minmax(0,1.4fr)_5.5rem_8rem_9rem_10rem] gap-4 border-b px-5 py-2.5 text-xs font-medium text-muted-foreground @4xl/content:grid'>
          <span>Player</span>
          <span>Konami ID claimed</span>
          <span>Evidence</span>
          <span>Requested</span>
          <span>Status</span>
          <span />
        </div>

        {list.isLoading ? (
          <div className='grid h-48 place-items-center'>
            <Loader2 className='animate-spin text-muted-foreground' />
          </div>
        ) : list.error ? (
          <div className='p-6 text-sm text-destructive'>
            {errorMessage(list.error)}
          </div>
        ) : visible.length === 0 ? (
          <div className='grid place-items-center gap-3 py-16 text-center'>
            <span className='grid size-14 place-items-center rounded-full bg-[#eef0fb] text-[#5b5bd6]'>
              <BadgeCheck className='size-6' />
            </span>
            <div className='font-semibold'>
              {search ? 'No matches' : 'No requests yet'}
            </div>
          </div>
        ) : (
          <ul className='divide-y divide-border'>
            {visible.map((item) => (
              <li
                key={item.id}
                onClick={() => setSelectedId(item.id)}
                className='grid cursor-pointer grid-cols-[1fr_auto] items-center gap-x-4 gap-y-2 px-5 py-3.5 transition-colors hover:bg-muted/50 @4xl/content:grid-cols-[minmax(0,2fr)_minmax(0,1.4fr)_5.5rem_8rem_9rem_10rem]'
              >
                <div className='flex min-w-0 items-center gap-3'>
                  <Avatar name={nameOf(item)} />
                  <div className='min-w-0'>
                    <div className='truncate font-semibold'>{nameOf(item)}</div>
                    <div className='truncate text-sm text-muted-foreground'>
                      {item.player.displayName}
                    </div>
                  </div>
                </div>
                <span className='hidden truncate font-mono text-sm @4xl/content:block'>
                  {item.player.konamiId ?? '—'}
                </span>
                <span className='hidden items-center gap-1 @4xl/content:flex'>
                  {item.evidenceIds.slice(0, 1).map((id) => (
                    <EvidenceImage key={id} id={id} size='thumb' />
                  ))}
                  {item.evidenceIds.length > 1 && (
                    <span className='text-xs text-muted-foreground'>
                      +{item.evidenceIds.length - 1}
                    </span>
                  )}
                  {item.evidenceIds.length === 0 && (
                    <span className='text-sm text-muted-foreground'>None</span>
                  )}
                </span>
                <span className='hidden text-sm text-muted-foreground @4xl/content:block'>
                  {ago(item.requestedAt)}
                </span>
                <span className='flex flex-col items-end gap-1 @4xl/content:items-start'>
                  <StatusBadge status={item.status} />
                </span>
                <span className='col-span-2 flex justify-end gap-2 @4xl/content:col-span-1'>
                  {isOpen(item.status) && (
                    <>
                      <Button
                        size='sm'
                        className='rounded-lg'
                        onClick={(event) => {
                          event.stopPropagation()
                          setDeciding({ item, decision: 'approve' })
                        }}
                      >
                        Approve
                      </Button>
                      <Button
                        size='sm'
                        variant='ghost'
                        className='rounded-lg text-destructive'
                        onClick={(event) => {
                          event.stopPropagation()
                          setDeciding({ item, decision: 'reject' })
                        }}
                      >
                        Reject
                      </Button>
                    </>
                  )}
                </span>
              </li>
            ))}
          </ul>
        )}
      </section>
      <LoadMore
        hasNextPage={list.hasNextPage}
        isFetchingNextPage={list.isFetchingNextPage}
        fetchNextPage={list.fetchNextPage}
      />
      <ReviewSheet
        item={selected}
        onClose={() => setSelectedId(null)}
        onDecide={(item, decision) => setDeciding({ item, decision })}
      />
      <DecisionDialog
        option={deciding ? options[deciding.decision] : null}
        description={
          deciding && (
            <>
              {nameOf(deciding.item)} ·{' '}
              <span className='font-mono'>{deciding.item.player.konamiId}</span>
            </>
          )
        }
        pending={decide.isPending}
        onCancel={() => setDeciding(null)}
        onConfirm={({ note }) => decide.mutate(note)}
      />
    </Page>
  )
}

function Avatar({ name }: { name: string }) {
  return (
    <span className='grid size-10 shrink-0 place-items-center rounded-full bg-[#eef0fb] text-sm font-semibold text-[#4b4cd1]'>
      {initials(name) || '?'}
    </span>
  )
}

/** The claim beside the screenshots, so the reviewer can compare them. */
function ReviewSheet({
  item,
  onClose,
  onDecide,
}: {
  item: Verification | null
  onClose: () => void
  onDecide: (item: Verification, decision: Decision) => void
}) {
  return (
    <Sheet open={!!item} onOpenChange={(open) => !open && onClose()}>
      <SheetContent className='w-full gap-0 overflow-y-auto sm:max-w-xl'>
        <SheetHeader className='sr-only'>
          <SheetTitle>Verification request</SheetTitle>
        </SheetHeader>
        {item && (
          <div className='flex min-h-full flex-col'>
            <div className='bg-[linear-gradient(150deg,#eef0ff,#e3e7ff)] px-6 pt-10 pb-6 dark:bg-[linear-gradient(150deg,#1d1f45,#1a1c3d)]'>
              <StatusBadge status={item.status} />
              <div className='mt-4 text-xs font-medium tracking-wide text-muted-foreground uppercase'>
                Konami ID claimed
              </div>
              <button
                type='button'
                onClick={() => {
                  if (!item.player.konamiId) return
                  navigator.clipboard.writeText(item.player.konamiId)
                  toast.success('Copied.')
                }}
                className='mt-1 inline-flex items-center gap-2 font-mono text-3xl font-bold tracking-tight'
              >
                {item.player.konamiId ?? '—'}
                <Copy className='size-4 text-muted-foreground' />
              </button>
              <div className='mt-4 flex items-center gap-3'>
                <Avatar name={nameOf(item)} />
                <div className='min-w-0'>
                  <div className='truncate font-semibold'>{nameOf(item)}</div>
                  <div className='truncate text-sm text-muted-foreground'>
                    {item.player.displayName} ·{' '}
                    <span aria-hidden>
                      {countryFlag(item.player.countryCode)}
                    </span>{' '}
                    {countryName(item.player.countryCode)}
                  </div>
                </div>
              </div>
            </div>

            <div className='grid gap-5 p-6'>
              <dl className='grid gap-3 text-sm'>
                <Row label='Requested'>{dateTime(item.requestedAt)}</Row>
                {item.player.inGameName && (
                  <Row label='In-game name'>{item.player.inGameName}</Row>
                )}
                {item.player.previousRejections > 0 && (
                  <Row label='Rejected before'>
                    <span className='font-medium text-destructive'>
                      {item.player.previousRejections}×
                    </span>
                  </Row>
                )}
                {item.reviewedAt && (
                  <Row label='Decided'>{dateTime(item.reviewedAt)}</Row>
                )}
              </dl>
              {item.playerNote && (
                <div className='rounded-xl bg-muted p-3 text-sm'>
                  “{item.playerNote}”
                </div>
              )}
              {item.decisionReason && (
                <div className='rounded-xl border p-3 text-sm'>
                  <div className='text-xs text-muted-foreground'>Decision</div>
                  {item.decisionReason}
                </div>
              )}
              <div className='grid gap-3'>
                <h3 className='text-sm font-semibold'>
                  Evidence
                  <span className='ms-2 font-normal text-muted-foreground'>
                    Check the Konami ID on screen matches
                  </span>
                </h3>
                {item.evidenceIds.length === 0 ? (
                  <p className='text-sm text-muted-foreground'>
                    No screenshots.
                  </p>
                ) : (
                  item.evidenceIds.map((id) => (
                    <EvidenceImage key={id} id={id} size='full' />
                  ))
                )}
              </div>
            </div>

            {isOpen(item.status) && (
              <div className='sticky bottom-0 mt-auto flex gap-2 border-t bg-background p-4'>
                <Button
                  variant='ghost'
                  className='h-11 flex-1 rounded-xl text-destructive'
                  onClick={() => onDecide(item, 'reject')}
                >
                  Reject
                </Button>
                <Button
                  className='h-11 flex-[2] rounded-xl'
                  onClick={() => onDecide(item, 'approve')}
                >
                  Approve
                </Button>
              </div>
            )}
          </div>
        )}
      </SheetContent>
    </Sheet>
  )
}

function Row({
  label,
  children,
}: {
  label: string
  children: React.ReactNode
}) {
  return (
    <div className='flex items-center justify-between gap-4'>
      <dt className='text-muted-foreground'>{label}</dt>
      <dd className='min-w-0 text-end'>{children}</dd>
    </div>
  )
}
