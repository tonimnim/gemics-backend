import { useMemo, useState } from 'react'
import { useMutation, useQuery, useQueryClient } from '@tanstack/react-query'
import {
  CheckCircle2,
  Clock3,
  Copy,
  Inbox,
  Loader2,
  MoreHorizontal,
  Search,
} from 'lucide-react'
import { toast } from 'sonner'
import { useCan } from '@/stores/auth-store'
import { api, errorMessage } from '@/lib/api'
import { ago, dateTime, initials, money, usd } from '@/lib/format'
import { usePagedList } from '@/lib/paged'
import { cn } from '@/lib/utils'
import { Button } from '@/components/ui/button'
import {
  Dialog,
  DialogContent,
  DialogFooter,
  DialogHeader,
  DialogTitle,
} from '@/components/ui/dialog'
import {
  DropdownMenu,
  DropdownMenuContent,
  DropdownMenuItem,
  DropdownMenuTrigger,
} from '@/components/ui/dropdown-menu'
import {
  Sheet,
  SheetContent,
  SheetHeader,
  SheetTitle,
} from '@/components/ui/sheet'
import { Textarea } from '@/components/ui/textarea'
import { LoadMore } from '@/components/list-state'
import { Page } from '@/components/page'

type Status =
  | 'requested'
  | 'approved'
  | 'processing'
  | 'manual_review'
  | 'succeeded'
  | 'rejected'
  | 'failed'
type Stage = 'action' | 'processing' | 'done'
type Decision = 'approve' | 'reject' | 'mark_succeeded' | 'mark_failed'

type Refund = {
  id: string
  paymentId: string
  amountMinor: number
  currency: string
  reasonCode: string
  mandatory: boolean
  playerNote: string
  status: Status
  providerReceipt: string | null
  providerResultDescription: string | null
  requestedAt: string
  reviewedAt: string | null
  completedAt: string | null
  playerDisplayName: string
  playerHandle: string | null
  phoneNumber: string
  competitionName: string
}

/** A stage's count and USD total; unconverted refunds have no rate yet. */
type StageSummary = { count: number; amountMinor: number; unconverted: number }

type Summary = {
  action: StageSummary
  processing: StageSummary
  refundedLast30Days: StageSummary
  currency: string
}

const statusLook: Record<Status, { label: string; dot: string; pill: string }> =
  {
    requested: {
      label: 'Awaiting approval',
      dot: 'bg-[#f5a623]',
      pill: 'bg-[#fff4e0] text-[#9a6200]',
    },
    manual_review: {
      label: 'Needs review',
      dot: 'bg-[#ff8a65]',
      pill: 'bg-[#ffece6] text-[#b4441f]',
    },
    approved: {
      label: 'Ready to pay out',
      dot: 'bg-[#5b5bd6]',
      pill: 'bg-[#eef0fb] text-[#4b4cd1]',
    },
    failed: {
      label: 'Payout failed',
      dot: 'bg-[#e5484d]',
      pill: 'bg-[#ffe9ea] text-[#c62a2f]',
    },
    processing: {
      label: 'Processing',
      dot: 'bg-[#3e8bff]',
      pill: 'bg-[#e6f0ff] text-[#1f63d1]',
    },
    succeeded: {
      label: 'Refunded',
      dot: 'bg-[#12a594]',
      pill: 'bg-[#e0f7f2] text-[#0b7d70]',
    },
    rejected: {
      label: 'Rejected',
      dot: 'bg-[#9a9db5]',
      pill: 'bg-muted text-muted-foreground',
    },
  }

const reasonLabels: Record<string, string> = {
  player_withdrawal: 'Player withdrew',
  competition_cancelled: 'Competition cancelled',
  duplicate_payment: 'Duplicate payment',
  operations_adjustment: 'Adjustment',
}

const decisionLabels: Record<Decision, string> = {
  approve: 'Approve',
  reject: 'Reject',
  mark_succeeded: 'Record payout',
  mark_failed: 'Mark failed',
}

/** The one obvious next step for a refund, then everything else it allows. */
function actionsFor(refund: Refund): {
  primary?: Decision
  others: Decision[]
} {
  const reject: Decision[] = refund.mandatory ? [] : ['reject']
  switch (refund.status) {
    case 'requested':
      return { primary: 'approve', others: reject }
    case 'approved':
    case 'manual_review':
      return { primary: 'mark_succeeded', others: ['mark_failed', ...reject] }
    case 'processing':
      return { primary: 'mark_succeeded', others: ['mark_failed'] }
    case 'failed':
      return { primary: 'approve', others: ['mark_succeeded'] }
    default:
      return { others: [] }
  }
}

function StatusPill({ status }: { status: Status }) {
  const look = statusLook[status]
  return (
    <span
      className={cn(
        'inline-flex items-center gap-1.5 rounded-full px-2.5 py-1 text-xs font-medium',
        look.pill
      )}
    >
      <span className={cn('size-1.5 rounded-full', look.dot)} />
      {look.label}
    </span>
  )
}

function Avatar({ name }: { name: string }) {
  return (
    <span className='grid size-10 shrink-0 place-items-center rounded-full bg-[#eef0fb] text-sm font-semibold text-[#4b4cd1]'>
      {initials(name) || '?'}
    </span>
  )
}

const stageCards: {
  stage: Stage
  label: string
  icon: React.ElementType
  tone: string
}[] = [
  {
    stage: 'action',
    label: 'Needs action',
    icon: Inbox,
    tone: 'bg-[#fff4e0] text-[#d98a00]',
  },
  {
    stage: 'processing',
    label: 'Processing',
    icon: Clock3,
    tone: 'bg-[#e6f0ff] text-[#1f63d1]',
  },
  {
    stage: 'done',
    label: 'Refunded · 30 days',
    icon: CheckCircle2,
    tone: 'bg-[#e0f7f2] text-[#0b7d70]',
  },
]

/** Count and total per stage. A summary only: the list below shows every refund. */
function StageCards({ summary }: { summary?: Summary }) {
  const values = summary && {
    action: summary.action,
    processing: summary.processing,
    done: summary.refundedLast30Days,
  }
  return (
    <div className='grid gap-4 sm:grid-cols-3'>
      {stageCards.map((card) => {
        const value = values?.[card.stage]
        return (
          <div key={card.stage} className='rounded-2xl bg-card p-5'>
            <span
              className={cn(
                'grid size-10 place-items-center rounded-xl',
                card.tone
              )}
            >
              <card.icon className='size-5' />
            </span>
            <div className='mt-4 text-sm text-muted-foreground'>
              {card.label}
            </div>
            <div className='mt-1 flex items-baseline gap-2'>
              <span className='text-3xl font-bold tracking-tight'>
                {value?.count ?? '–'}
              </span>
              {value && value.amountMinor > 0 && (
                <span className='text-sm text-muted-foreground'>
                  {usd(value.amountMinor)}
                </span>
              )}
            </div>
            {value && value.unconverted > 0 && (
              <div className='mt-1 text-xs text-[#c4421d]'>
                {value.unconverted} awaiting an exchange rate
              </div>
            )}
          </div>
        )
      })}
    </div>
  )
}

function CopyValue({ value }: { value: string }) {
  return (
    <button
      type='button'
      onClick={() => {
        navigator.clipboard.writeText(value)
        toast.success('Copied.')
      }}
      className='inline-flex items-center gap-1.5 rounded-lg bg-muted px-2 py-1 font-mono text-sm hover:bg-accent'
    >
      {value}
      <Copy className='size-3.5 text-muted-foreground' />
    </button>
  )
}

/** Confirms a decision; recording a payout asks for the M-Pesa receipt. */
function DecisionDialog({
  refund,
  decision,
  pending,
  onCancel,
  onConfirm,
}: {
  refund: Refund | null
  decision: Decision | null
  pending: boolean
  onCancel: () => void
  onConfirm: (input: { note: string; receipt: string }) => void
}) {
  return (
    <Dialog
      open={!!refund && !!decision}
      onOpenChange={(open) => !open && onCancel()}
    >
      <DialogContent className='rounded-2xl sm:max-w-md'>
        {refund && decision && (
          <DecisionForm
            key={`${refund.id}-${decision}`}
            refund={refund}
            decision={decision}
            pending={pending}
            onConfirm={onConfirm}
          />
        )}
      </DialogContent>
    </Dialog>
  )
}

function DecisionForm({
  refund,
  decision,
  pending,
  onConfirm,
}: {
  refund: Refund
  decision: Decision
  pending: boolean
  onConfirm: (input: { note: string; receipt: string }) => void
}) {
  const [note, setNote] = useState('')
  const [receipt, setReceipt] = useState('')
  const needsNote = decision === 'reject' || decision === 'mark_failed'
  const payout = decision === 'mark_succeeded'
  const ready = (!needsNote || note.trim()) && (!payout || receipt.trim())
  return (
    <>
      <DialogHeader>
        <DialogTitle>{decisionLabels[decision]}</DialogTitle>
      </DialogHeader>
      <div className='rounded-xl bg-muted p-4'>
        <div className='text-3xl font-bold tracking-tight'>
          {money(refund.amountMinor, refund.currency)}
        </div>
        <div className='mt-1 text-sm text-muted-foreground'>
          {refund.playerDisplayName}
        </div>
        {payout && (
          <div className='mt-3'>
            <CopyValue value={refund.phoneNumber} />
          </div>
        )}
      </div>
      <div className='grid gap-3'>
        {payout && (
          <input
            autoFocus
            value={receipt}
            onChange={(event) => setReceipt(event.target.value.toUpperCase())}
            placeholder='M-Pesa receipt'
            maxLength={128}
            className='h-11 rounded-xl border border-input bg-card px-4 font-mono text-sm outline-none focus:border-primary focus:ring-4 focus:ring-primary/15'
          />
        )}
        {(needsNote || payout) && (
          <Textarea
            value={note}
            onChange={(event) => setNote(event.target.value)}
            placeholder={needsNote ? 'Reason' : 'Note (optional)'}
            maxLength={1000}
            className='min-h-20 rounded-xl'
          />
        )}
      </div>
      <DialogFooter>
        <Button
          className='h-11 w-full rounded-xl'
          variant={
            decision === 'reject' || decision === 'mark_failed'
              ? 'destructive'
              : 'default'
          }
          disabled={pending || !ready}
          onClick={() =>
            onConfirm({ note: note.trim(), receipt: receipt.trim() })
          }
        >
          {pending && <Loader2 className='animate-spin' />}
          {decisionLabels[decision]}
        </Button>
      </DialogFooter>
    </>
  )
}

function Timeline({ refund }: { refund: Refund }) {
  const steps = [
    { label: 'Requested', at: refund.requestedAt },
    {
      label: refund.status === 'rejected' ? 'Rejected' : 'Reviewed',
      at: refund.reviewedAt,
    },
    {
      label: refund.status === 'failed' ? 'Payout failed' : 'Refunded',
      at: refund.completedAt,
    },
  ]
  return (
    <ol className='grid gap-4'>
      {steps.map((step, index) => (
        <li key={step.label} className='flex gap-3'>
          <div className='flex flex-col items-center'>
            <span
              className={cn(
                'size-2.5 rounded-full',
                step.at ? 'bg-primary' : 'bg-border'
              )}
            />
            {index < steps.length - 1 && (
              <span className='mt-1 w-px flex-1 bg-border' />
            )}
          </div>
          <div className='-mt-1 pb-1'>
            <div
              className={cn(
                'text-sm font-medium',
                !step.at && 'text-muted-foreground'
              )}
            >
              {step.label}
            </div>
            <div className='text-xs text-muted-foreground'>
              {step.at ? dateTime(step.at) : 'Not yet'}
            </div>
          </div>
        </li>
      ))}
    </ol>
  )
}

function RefundSheet({
  refund,
  canManage,
  onClose,
  onDecide,
}: {
  refund: Refund | null
  canManage: boolean
  onClose: () => void
  onDecide: (refund: Refund, decision: Decision) => void
}) {
  const actions =
    refund && canManage ? actionsFor(refund) : { others: [] as Decision[] }
  return (
    <Sheet open={!!refund} onOpenChange={(open) => !open && onClose()}>
      <SheetContent className='w-full gap-0 overflow-y-auto sm:max-w-md'>
        <SheetHeader className='sr-only'>
          <SheetTitle>Refund</SheetTitle>
        </SheetHeader>
        {refund && (
          <div className='flex h-full flex-col'>
            <div className='bg-[linear-gradient(150deg,#eef0ff,#e3e7ff)] px-6 pt-10 pb-6 dark:bg-[linear-gradient(150deg,#1d1f45,#1a1c3d)]'>
              <StatusPill status={refund.status} />
              <div className='mt-4 text-4xl font-bold tracking-tight'>
                {money(refund.amountMinor, refund.currency)}
              </div>
              <div className='mt-1 text-sm text-muted-foreground'>
                {reasonLabels[refund.reasonCode] ?? refund.reasonCode}
                {refund.mandatory && ' · Mandatory'}
              </div>
            </div>
            <div className='grid gap-6 p-6'>
              <div className='flex items-center gap-3'>
                <Avatar name={refund.playerDisplayName} />
                <div className='min-w-0'>
                  <div className='truncate font-semibold'>
                    {refund.playerDisplayName}
                  </div>
                  <div className='truncate text-sm text-muted-foreground'>
                    {refund.competitionName}
                  </div>
                </div>
              </div>
              <dl className='grid gap-3 text-sm'>
                <div className='flex items-center justify-between gap-4'>
                  <dt className='text-muted-foreground'>M-Pesa number</dt>
                  <dd>
                    <CopyValue value={refund.phoneNumber} />
                  </dd>
                </div>
                {refund.providerReceipt && (
                  <div className='flex items-center justify-between gap-4'>
                    <dt className='text-muted-foreground'>Receipt</dt>
                    <dd>
                      <CopyValue value={refund.providerReceipt} />
                    </dd>
                  </div>
                )}
                {refund.playerNote && (
                  <div className='rounded-xl bg-muted p-3 text-sm'>
                    “{refund.playerNote}”
                  </div>
                )}
                {refund.providerResultDescription && (
                  <div className='text-xs text-muted-foreground'>
                    {refund.providerResultDescription}
                  </div>
                )}
              </dl>
              <Timeline refund={refund} />
            </div>
            {actions.primary && (
              <div className='mt-auto grid gap-2 border-t p-6'>
                <Button
                  className='h-11 rounded-xl'
                  onClick={() => onDecide(refund, actions.primary!)}
                >
                  {decisionLabels[actions.primary]}
                </Button>
                <div className='flex gap-2'>
                  {actions.others.map((decision) => (
                    <Button
                      key={decision}
                      variant='ghost'
                      className={cn(
                        'h-10 flex-1 rounded-xl',
                        decision !== 'mark_succeeded' && 'text-destructive'
                      )}
                      onClick={() => onDecide(refund, decision)}
                    >
                      {decisionLabels[decision]}
                    </Button>
                  ))}
                </div>
              </div>
            )}
          </div>
        )}
      </SheetContent>
    </Sheet>
  )
}

export function RefundsPage() {
  const queryClient = useQueryClient()
  const canManage = useCan('refund.manage')
  const [search, setSearch] = useState('')
  const [selected, setSelected] = useState<Refund | null>(null)
  const [deciding, setDeciding] = useState<{
    refund: Refund
    decision: Decision
  } | null>(null)
  const summary = useQuery({
    queryKey: ['refunds', 'summary'],
    queryFn: () => api<Summary>('/v1/admin/refunds/summary'),
  })
  const list = usePagedList<Refund>(['refunds'], '/v1/admin/refunds', {
    status: 'all',
  })
  const visible = useMemo(() => {
    const term = search.trim().toLowerCase()
    if (!term) return list.items
    return list.items.filter((refund) =>
      [
        refund.playerDisplayName,
        refund.playerHandle,
        refund.phoneNumber,
        refund.competitionName,
        refund.providerReceipt,
      ]
        .filter(Boolean)
        .some((value) => value!.toLowerCase().includes(term))
    )
  }, [list.items, search])

  const decide = useMutation({
    mutationFn: ({ note, receipt }: { note: string; receipt: string }) =>
      api(`/v1/admin/refunds/${deciding!.refund.id}/decisions`, {
        method: 'POST',
        idempotent: true,
        body: {
          decision: deciding!.decision,
          note,
          ...(deciding!.decision === 'mark_succeeded'
            ? { providerReceipt: receipt }
            : {}),
        },
      }),
    onSuccess: () => {
      toast.success('Refund updated.')
      setDeciding(null)
      setSelected(null)
      queryClient.invalidateQueries({ queryKey: ['refunds'] })
      queryClient.invalidateQueries({ queryKey: ['overview'] })
    },
    onError: (error) => toast.error(errorMessage(error)),
  })
  const openDecision = (refund: Refund, decision: Decision) =>
    setDeciding({ refund, decision })

  return (
    <Page title='Refunds' permission='refund.view'>
      <div className='grid gap-5'>
        <StageCards summary={summary.data} />

        <section className='overflow-hidden rounded-2xl bg-card'>
          <div className='flex flex-wrap items-center justify-between gap-3 border-b px-5 py-4'>
            <div className='flex items-center gap-2'>
              <h2 className='font-semibold'>All refunds</h2>
              <span className='rounded-full bg-muted px-2 py-0.5 text-xs font-medium text-muted-foreground'>
                {visible.length}
                {list.hasNextPage ? '+' : ''}
              </span>
            </div>
            <label className='relative block w-full sm:w-72'>
              <Search className='pointer-events-none absolute start-3.5 top-1/2 size-4 -translate-y-1/2 text-muted-foreground' />
              <input
                value={search}
                onChange={(event) => setSearch(event.target.value)}
                placeholder='Search player, phone or receipt'
                className='h-10 w-full rounded-xl border border-transparent bg-muted ps-10 pe-4 text-sm outline-none focus:border-primary focus:bg-card focus:ring-4 focus:ring-primary/15'
              />
            </label>
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
                <Inbox className='size-6' />
              </span>
              <div className='font-semibold'>
                {search ? 'No matches' : 'No refunds yet'}
              </div>
            </div>
          ) : (
            <ul className='divide-y divide-border'>
              {visible.map((refund) => {
                const actions = canManage
                  ? actionsFor(refund)
                  : { others: [] as Decision[] }
                return (
                  <li
                    key={refund.id}
                    onClick={() => setSelected(refund)}
                    className='grid cursor-pointer grid-cols-[1fr_auto] items-center gap-x-4 gap-y-2 px-5 py-4 transition-colors hover:bg-muted/50 @4xl/content:grid-cols-[minmax(0,2fr)_minmax(0,1.4fr)_7rem_10rem_11rem]'
                  >
                    <div className='flex min-w-0 items-center gap-3'>
                      <Avatar name={refund.playerDisplayName} />
                      <div className='min-w-0'>
                        <div className='truncate font-semibold'>
                          {refund.playerDisplayName}
                        </div>
                        <div className='truncate text-sm text-muted-foreground'>
                          {refund.competitionName}
                        </div>
                      </div>
                    </div>
                    <div className='hidden min-w-0 @4xl/content:block'>
                      <div className='truncate text-sm'>
                        {reasonLabels[refund.reasonCode] ?? refund.reasonCode}
                      </div>
                      <div className='text-xs text-muted-foreground'>
                        {ago(refund.requestedAt)}
                        {refund.mandatory && ' · Mandatory'}
                      </div>
                    </div>
                    <div className='text-end font-semibold @4xl/content:text-start'>
                      {money(refund.amountMinor, refund.currency)}
                    </div>
                    <div>
                      <StatusPill status={refund.status} />
                    </div>
                    <div
                      className='flex items-center justify-end gap-1'
                      onClick={(event) => event.stopPropagation()}
                    >
                      {actions.primary && (
                        <Button
                          size='sm'
                          className='rounded-lg'
                          onClick={() => openDecision(refund, actions.primary!)}
                        >
                          {decisionLabels[actions.primary]}
                        </Button>
                      )}
                      {actions.others.length > 0 && (
                        <DropdownMenu>
                          <DropdownMenuTrigger asChild>
                            <Button
                              size='icon'
                              variant='ghost'
                              className='size-8 rounded-lg'
                              aria-label='More actions'
                            >
                              <MoreHorizontal />
                            </Button>
                          </DropdownMenuTrigger>
                          <DropdownMenuContent
                            align='end'
                            className='rounded-xl'
                          >
                            {actions.others.map((decision) => (
                              <DropdownMenuItem
                                key={decision}
                                variant={
                                  decision === 'mark_succeeded'
                                    ? 'default'
                                    : 'destructive'
                                }
                                onClick={() => openDecision(refund, decision)}
                              >
                                {decisionLabels[decision]}
                              </DropdownMenuItem>
                            ))}
                          </DropdownMenuContent>
                        </DropdownMenu>
                      )}
                    </div>
                  </li>
                )
              })}
            </ul>
          )}
        </section>
        <LoadMore
          hasNextPage={list.hasNextPage}
          isFetchingNextPage={list.isFetchingNextPage}
          fetchNextPage={list.fetchNextPage}
        />
      </div>

      <RefundSheet
        refund={selected}
        canManage={canManage}
        onClose={() => setSelected(null)}
        onDecide={openDecision}
      />
      <DecisionDialog
        refund={deciding?.refund ?? null}
        decision={deciding?.decision ?? null}
        pending={decide.isPending}
        onCancel={() => setDeciding(null)}
        onConfirm={(input) => decide.mutate(input)}
      />
    </Page>
  )
}
