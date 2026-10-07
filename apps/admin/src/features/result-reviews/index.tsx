import { useState } from 'react'
import { Link } from '@tanstack/react-router'
import { ago, humanize } from '@/lib/format'
import { usePagedList } from '@/lib/paged'
import {
  Table,
  TableBody,
  TableCell,
  TableHead,
  TableHeader,
  TableRow,
} from '@/components/ui/table'
import { FilterTabs } from '@/components/filter-tabs'
import { ListRows, LoadMore } from '@/components/list-state'
import { Page } from '@/components/page'
import { StatusBadge } from '@/components/status-badge'
import type { ReviewSummary } from './api'

const filters = ['queued', 'decided', 'closed'] as const

export function ResultReviewsPage() {
  const [status, setStatus] = useState<string>('queued')
  const list = usePagedList<ReviewSummary>(
    ['result-reviews'],
    '/v1/admin/result-reviews',
    { status }
  )
  return (
    <Page title='Result reviews' permission='result_review.manage'>
      <FilterTabs value={status} options={filters} onChange={setStatus} />
      <div className='rounded-md border'>
        <Table>
          <TableHeader>
            <TableRow>
              <TableHead>Match</TableHead>
              <TableHead>Reason</TableHead>
              <TableHead>Status</TableHead>
              <TableHead>Queued</TableHead>
            </TableRow>
          </TableHeader>
          <TableBody>
            <ListRows
              isLoading={list.isLoading}
              error={list.error}
              empty='Nothing waiting. Nice.'
              columns={4}
              count={list.items.length}
            />
            {list.items.map((review) => (
              <TableRow key={review.id} className='cursor-pointer'>
                <TableCell>
                  <Link
                    to='/result-reviews/$id'
                    params={{ id: review.id }}
                    className='block'
                  >
                    <div className='font-medium'>
                      {review.match.competitionName}
                    </div>
                    <div className='text-xs text-muted-foreground'>
                      {humanize(review.match.bracket)} · Round{' '}
                      {review.match.roundNumber} · Match{' '}
                      {review.match.matchNumber}
                    </div>
                  </Link>
                </TableCell>
                <TableCell className='text-sm'>
                  {humanize(review.reason)}
                </TableCell>
                <TableCell>
                  <StatusBadge status={review.status} />
                  {review.decision && (
                    <div className='mt-1 text-xs text-muted-foreground'>
                      {humanize(review.decision)}
                    </div>
                  )}
                </TableCell>
                <TableCell className='text-sm text-muted-foreground'>
                  {ago(review.queuedAt)}
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
    </Page>
  )
}
