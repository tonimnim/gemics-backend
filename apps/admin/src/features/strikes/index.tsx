import { useState } from 'react'
import { useMutation, useQueryClient } from '@tanstack/react-query'
import { toast } from 'sonner'
import { api } from '@/lib/api'
import { dateTime, humanize, shortId } from '@/lib/format'
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
import { DecisionDialog } from '@/components/decision-dialog'
import { ListRows, LoadMore } from '@/components/list-state'
import { Page } from '@/components/page'
import { StatusBadge } from '@/components/status-badge'

type Strike = {
  id: string
  userId: string
  matchId: string
  reasonCode: string
  note: string
  createdAt: string
  revokedAt: string | null
  revokeReason: string | null
}

export function StrikesPage() {
  const queryClient = useQueryClient()
  const [revoking, setRevoking] = useState<Strike | null>(null)
  const list = usePagedList<Strike>(['strikes'], '/v1/admin/player-strikes', {
    status: 'all',
  })
  const revoke = useMutation({
    mutationFn: (reason: string) =>
      api(`/v1/admin/player-strikes/${revoking!.id}/revocations`, {
        method: 'POST',
        idempotent: true,
        body: { reason },
      }),
    onSuccess: () => {
      toast.success('Strike revoked.')
      setRevoking(null)
      queryClient.invalidateQueries({ queryKey: ['strikes'] })
    },
  })

  return (
    <Page title='Conduct strikes' permission='player_strike.revoke'>
      <div className='rounded-md border'>
        <Table>
          <TableHeader>
            <TableRow>
              <TableHead>Player</TableHead>
              <TableHead>Reason</TableHead>
              <TableHead>Given</TableHead>
              <TableHead>Status</TableHead>
              <TableHead className='text-end' />
            </TableRow>
          </TableHeader>
          <TableBody>
            <ListRows
              isLoading={list.isLoading}
              error={list.error}
              empty='No strikes here.'
              columns={5}
              count={list.items.length}
            />
            {list.items.map((strike) => (
              <TableRow key={strike.id}>
                <TableCell className='font-mono text-xs'>
                  {shortId(strike.userId)}
                </TableCell>
                <TableCell>
                  <div className='text-sm'>{humanize(strike.reasonCode)}</div>
                  <div className='max-w-sm text-xs text-muted-foreground'>
                    {strike.note}
                  </div>
                </TableCell>
                <TableCell className='text-sm text-muted-foreground'>
                  {dateTime(strike.createdAt)}
                </TableCell>
                <TableCell>
                  <StatusBadge
                    status={strike.revokedAt ? 'revoked' : 'active'}
                  />
                  {strike.revokeReason && (
                    <div className='mt-1 max-w-xs text-xs text-muted-foreground'>
                      {strike.revokeReason}
                    </div>
                  )}
                </TableCell>
                <TableCell className='text-end'>
                  {!strike.revokedAt && (
                    <Button
                      size='sm'
                      variant='outline'
                      onClick={() => setRevoking(strike)}
                    >
                      Revoke
                    </Button>
                  )}
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
      <DecisionDialog
        option={
          revoking
            ? {
                label: 'Revoke strike',
                noteLabel: 'Why is this strike wrong?',
                noteRequired: true,
              }
            : null
        }
        pending={revoke.isPending}
        onCancel={() => setRevoking(null)}
        onConfirm={({ note }) => revoke.mutate(note)}
      />
    </Page>
  )
}
