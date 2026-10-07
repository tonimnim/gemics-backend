import { useState } from 'react'
import { useMutation, useQuery, useQueryClient } from '@tanstack/react-query'
import { Loader2 } from 'lucide-react'
import { toast } from 'sonner'
import { api, errorMessage } from '@/lib/api'
import { ago, dateTime, money } from '@/lib/format'
import { usePagedList } from '@/lib/paged'
import { Alert, AlertDescription } from '@/components/ui/alert'
import { Button } from '@/components/ui/button'
import {
  Sheet,
  SheetContent,
  SheetDescription,
  SheetHeader,
  SheetTitle,
} from '@/components/ui/sheet'
import {
  Table,
  TableBody,
  TableCell,
  TableHead,
  TableHeader,
  TableRow,
} from '@/components/ui/table'
import {
  DecisionDialog,
  type DecisionOption,
} from '@/components/decision-dialog'
import { FilterTabs } from '@/components/filter-tabs'
import { ListRows, LoadMore } from '@/components/list-state'
import { Page } from '@/components/page'
import { StatusBadge } from '@/components/status-badge'

type Payment = {
  id: string
  playerDisplayName: string
  competitionName: string
  amountMinor: number
  currency: string
  phoneNumber: string
  status: string
  checkoutRequestId: string | null
  providerReceipt: string | null
  providerResultCode: string | null
  providerResultDescription: string | null
  queryAttempts: number
  lastQueryAt: string | null
  createdAt: string
  callbackCount: number
  pendingCallbackCount: number
}

type PaymentDetail = {
  data: {
    payment: Payment
    callbackEvents: {
      id: number
      receivedAt: string
      processedAt: string | null
      processingError: string | null
    }[]
    actions: { id: number; action: string; occurredAt: string }[]
  }
}

type Decision = 'retry_query' | 'replay_callbacks' | 'mark_failed'
const filters = [
  'review',
  'pending',
  'callback_received',
  'succeeded',
  'failed',
] as const
const options: Record<Decision, DecisionOption> = {
  retry_query: { label: 'Ask M-Pesa again', noteLabel: 'Note' },
  replay_callbacks: { label: 'Replay received callbacks', noteLabel: 'Note' },
  mark_failed: {
    label: 'Mark payment failed',
    destructive: true,
    noteRequired: true,
    noteLabel: 'Why it failed',
    warning: 'Only when M-Pesa confirms no money moved. The place is released.',
  },
}

function PaymentSheet({
  id,
  onClose,
}: {
  id: string | null
  onClose: () => void
}) {
  const queryClient = useQueryClient()
  const [decision, setDecision] = useState<Decision | null>(null)
  const { data, isLoading, error } = useQuery({
    queryKey: ['payment-review', id],
    queryFn: () => api<PaymentDetail>(`/v1/admin/payment-reviews/${id}`),
    enabled: !!id,
  })
  const decide = useMutation({
    mutationFn: (note: string) =>
      api(`/v1/admin/payment-reviews/${id}/decisions`, {
        method: 'POST',
        idempotent: true,
        body: { decision, note },
      }),
    onSuccess: () => {
      toast.success('Done. The payment updates as M-Pesa answers.')
      setDecision(null)
      queryClient.invalidateQueries({ queryKey: ['payment-review', id] })
      queryClient.invalidateQueries({ queryKey: ['payment-reviews'] })
      queryClient.invalidateQueries({ queryKey: ['overview'] })
    },
  })
  const payment = data?.data.payment
  const actions = data?.data.actions ?? []
  return (
    <Sheet open={!!id} onOpenChange={(open) => !open && onClose()}>
      <SheetContent className='w-full overflow-y-auto sm:max-w-lg'>
        <SheetHeader>
          <SheetTitle>Payment</SheetTitle>
          <SheetDescription>
            {payment &&
              `${payment.playerDisplayName} · ${payment.competitionName}`}
          </SheetDescription>
        </SheetHeader>
        <div className='space-y-6 px-4 pb-6'>
          {isLoading && <Loader2 className='animate-spin' />}
          {error && (
            <Alert variant='destructive'>
              <AlertDescription>{errorMessage(error)}</AlertDescription>
            </Alert>
          )}
          {payment && (
            <>
              <dl className='grid grid-cols-2 gap-3 text-sm'>
                <dt className='text-muted-foreground'>Status</dt>
                <dd>
                  <StatusBadge status={payment.status} />
                </dd>
                <dt className='text-muted-foreground'>Amount</dt>
                <dd>{money(payment.amountMinor, payment.currency)}</dd>
                <dt className='text-muted-foreground'>Phone</dt>
                <dd className='font-mono'>{payment.phoneNumber}</dd>
                <dt className='text-muted-foreground'>Receipt</dt>
                <dd className='font-mono'>{payment.providerReceipt ?? '—'}</dd>
                <dt className='text-muted-foreground'>M-Pesa result</dt>
                <dd>
                  {payment.providerResultCode ?? '—'}{' '}
                  {payment.providerResultDescription}
                </dd>
                <dt className='text-muted-foreground'>Checkout ID</dt>
                <dd className='font-mono text-xs break-all'>
                  {payment.checkoutRequestId ?? '—'}
                </dd>
                <dt className='text-muted-foreground'>Status checks</dt>
                <dd>
                  {payment.queryAttempts} (last {ago(payment.lastQueryAt)})
                </dd>
                <dt className='text-muted-foreground'>Callbacks</dt>
                <dd>
                  {payment.callbackCount} received,{' '}
                  {payment.pendingCallbackCount} unprocessed
                </dd>
                <dt className='text-muted-foreground'>Started</dt>
                <dd>{dateTime(payment.createdAt)}</dd>
              </dl>
              {payment.status === 'review' && (
                <div className='flex flex-wrap gap-2'>
                  <Button size='sm' onClick={() => setDecision('retry_query')}>
                    Ask M-Pesa again
                  </Button>
                  {payment.callbackCount > 0 && (
                    <Button
                      size='sm'
                      variant='outline'
                      onClick={() => setDecision('replay_callbacks')}
                    >
                      Replay callbacks
                    </Button>
                  )}
                  <Button
                    size='sm'
                    variant='outline'
                    className='text-destructive'
                    onClick={() => setDecision('mark_failed')}
                  >
                    Mark failed
                  </Button>
                </div>
              )}
              {actions.length > 0 && (
                <div>
                  <h3 className='mb-2 font-mono text-xs text-muted-foreground uppercase'>
                    History
                  </h3>
                  <ul className='space-y-1 text-sm'>
                    {actions.map((event) => (
                      <li key={event.id} className='flex justify-between gap-4'>
                        <span>{event.action}</span>
                        <span className='text-muted-foreground'>
                          {dateTime(event.occurredAt)}
                        </span>
                      </li>
                    ))}
                  </ul>
                </div>
              )}
            </>
          )}
        </div>
        <DecisionDialog
          option={decision ? options[decision] : null}
          pending={decide.isPending}
          onCancel={() => setDecision(null)}
          onConfirm={({ note }) => decide.mutate(note)}
        />
      </SheetContent>
    </Sheet>
  )
}

export function PaymentsPage() {
  const [status, setStatus] = useState<string>('review')
  const [selected, setSelected] = useState<string | null>(null)
  const list = usePagedList<Payment>(
    ['payment-reviews'],
    '/v1/admin/payment-reviews',
    { status }
  )
  return (
    <Page title='Payment reviews' permission='payment_review.manage'>
      <FilterTabs value={status} options={filters} onChange={setStatus} />
      <div className='rounded-md border'>
        <Table>
          <TableHeader>
            <TableRow>
              <TableHead>Player</TableHead>
              <TableHead>Competition</TableHead>
              <TableHead>Amount</TableHead>
              <TableHead>Status</TableHead>
              <TableHead>Started</TableHead>
            </TableRow>
          </TableHeader>
          <TableBody>
            <ListRows
              isLoading={list.isLoading}
              error={list.error}
              empty='No payments here.'
              columns={5}
              count={list.items.length}
            />
            {list.items.map((payment) => (
              <TableRow
                key={payment.id}
                className='cursor-pointer'
                onClick={() => setSelected(payment.id)}
              >
                <TableCell>
                  <div className='font-medium'>{payment.playerDisplayName}</div>
                  <div className='font-mono text-xs text-muted-foreground'>
                    {payment.phoneNumber}
                  </div>
                </TableCell>
                <TableCell className='text-sm'>
                  {payment.competitionName}
                </TableCell>
                <TableCell className='text-sm'>
                  {money(payment.amountMinor, payment.currency)}
                </TableCell>
                <TableCell>
                  <StatusBadge status={payment.status} />
                </TableCell>
                <TableCell className='text-sm text-muted-foreground'>
                  {ago(payment.createdAt)}
                </TableCell>
              </TableRow>
            ))}
          </TableBody>
        </Table>
      </div>
      <LoadMore
        hasNextPage={list.hasNextPage}
        isFetchingNextPage={list.isFetchingNextPage}
        fetchNextPage={list.fetchNextPage}
      />
      <PaymentSheet id={selected} onClose={() => setSelected(null)} />
    </Page>
  )
}
