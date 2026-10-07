import { useState } from 'react'
import { useMutation, useQueryClient } from '@tanstack/react-query'
import { toast } from 'sonner'
import { api } from '@/lib/api'
import { ago, shortId } from '@/lib/format'
import { usePagedList } from '@/lib/paged'
import { Button } from '@/components/ui/button'
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
import { EvidenceImage } from '@/components/evidence-image'
import { FilterTabs } from '@/components/filter-tabs'
import { ListRows, LoadMore } from '@/components/list-state'
import { Page } from '@/components/page'
import { StatusBadge } from '@/components/status-badge'

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
}

const filters = ['requested', 'under_review', 'approved', 'rejected'] as const
const options: Record<'approve' | 'reject', DecisionOption> = {
  approve: { label: 'Approve verification', noteLabel: 'Reason' },
  reject: {
    label: 'Reject verification',
    destructive: true,
    noteLabel: 'Reason shown to the player',
    noteRequired: true,
  },
}

export function VerificationsPage() {
  const queryClient = useQueryClient()
  const [status, setStatus] = useState<string>('requested')
  const [deciding, setDeciding] = useState<{
    item: Verification
    decision: 'approve' | 'reject'
  } | null>(null)
  const list = usePagedList<Verification>(
    ['verifications'],
    '/v1/admin/game-account-verifications',
    { status }
  )
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
  const open = status === 'requested' || status === 'under_review'

  return (
    <Page
      title='Account verifications'
      permission='game_account_verification.manage'
    >
      <FilterTabs value={status} options={filters} onChange={setStatus} />
      <div className='rounded-md border'>
        <Table>
          <TableHeader>
            <TableRow>
              <TableHead>Request</TableHead>
              <TableHead>Evidence</TableHead>
              <TableHead>Status</TableHead>
              {open && <TableHead className='text-end'>Decide</TableHead>}
            </TableRow>
          </TableHeader>
          <TableBody>
            <ListRows
              isLoading={list.isLoading}
              error={list.error}
              empty='No requests here.'
              columns={open ? 4 : 3}
              count={list.items.length}
            />
            {list.items.map((item) => (
              <TableRow key={item.id}>
                <TableCell className='align-top'>
                  <div className='font-mono text-xs'>
                    Player {shortId(item.userId)} · Account{' '}
                    {shortId(item.gameAccountId)}
                  </div>
                  <div className='mt-1 max-w-sm text-sm'>
                    {item.playerNote || (
                      <span className='text-muted-foreground'>No note</span>
                    )}
                  </div>
                  <div className='mt-1 text-xs text-muted-foreground'>
                    {ago(item.requestedAt)}
                  </div>
                </TableCell>
                <TableCell>
                  <div className='flex gap-2'>
                    {item.evidenceIds.length === 0 && (
                      <span className='text-sm text-muted-foreground'>
                        None
                      </span>
                    )}
                    {item.evidenceIds.map((id) => (
                      <EvidenceImage key={id} id={id} />
                    ))}
                  </div>
                </TableCell>
                <TableCell className='align-top'>
                  <StatusBadge status={item.status} />
                  {item.decisionReason && (
                    <div className='mt-1 max-w-xs text-xs text-muted-foreground'>
                      {item.decisionReason}
                    </div>
                  )}
                </TableCell>
                {open && (
                  <TableCell className='space-x-2 text-end align-top'>
                    <Button
                      size='sm'
                      onClick={() => setDeciding({ item, decision: 'approve' })}
                    >
                      Approve
                    </Button>
                    <Button
                      size='sm'
                      variant='outline'
                      onClick={() => setDeciding({ item, decision: 'reject' })}
                    >
                      Reject
                    </Button>
                  </TableCell>
                )}
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
      <DecisionDialog
        option={deciding ? options[deciding.decision] : null}
        pending={decide.isPending}
        onCancel={() => setDeciding(null)}
        onConfirm={({ note }) => decide.mutate(note)}
      />
    </Page>
  )
}
