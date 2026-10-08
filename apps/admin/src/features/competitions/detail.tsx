import { useQuery } from '@tanstack/react-query'
import { Link } from '@tanstack/react-router'
import { ArrowLeft, Loader2 } from 'lucide-react'
import { useAuthStore } from '@/stores/auth-store'
import { api, errorMessage } from '@/lib/api'
import { dateTime, humanize, money } from '@/lib/format'
import { usePagedList } from '@/lib/paged'
import { Alert, AlertDescription } from '@/components/ui/alert'
import { Button } from '@/components/ui/button'
import { Card, CardContent } from '@/components/ui/card'
import {
  Table,
  TableBody,
  TableCell,
  TableHead,
  TableHeader,
  TableRow,
} from '@/components/ui/table'
import { ListRows, LoadMore } from '@/components/list-state'
import { Page } from '@/components/page'
import { StatusBadge } from '@/components/status-badge'
import { competitionsPath, type Competition } from './api'

type Entry = {
  id: string
  displayName: string
  status: string
  seed: number | null
  checkedInAt: string | null
  createdAt: string
  playerId: string
  playerName: string
  handle: string | null
  inGameName: string | null
  paymentStatus: string | null
}

function Fact({ label, value }: { label: string; value: React.ReactNode }) {
  return (
    <div>
      <div className='font-mono text-xs text-muted-foreground uppercase'>
        {label}
      </div>
      <div className='mt-1 font-semibold'>{value}</div>
    </div>
  )
}

/**
 * One competition and everyone registered for it, with each entry's payment
 * status and amount, so staff can resolve registration problems. Totals and
 * revenue stay in the admin-only finance overview.
 */
export function CompetitionDetailPage({ id }: { id: string }) {
  const orgId = useAuthStore((state) => state.staff?.gamicsOrganizationId ?? '')
  const competition = useQuery({
    queryKey: ['competition', orgId, id],
    queryFn: () =>
      api<{ data: Competition }>(`${competitionsPath(orgId)}/${id}`),
    enabled: !!orgId,
  })
  const entries = usePagedList<Entry>(
    ['competition-entries', id],
    `${competitionsPath(orgId)}/${id}/entries`,
    {},
    !!orgId
  )
  const data = competition.data?.data
  const paid = (data?.entryFeeMinor ?? 0) > 0

  return (
    <Page
      title={data?.name ?? 'Competition'}
      description={data && `${humanize(data.format)} · eFootball Mobile`}
      permission='competition.manage'
      actions={
        <Button variant='outline' asChild>
          <Link to='/competitions'>
            <ArrowLeft /> All competitions
          </Link>
        </Button>
      }
    >
      {competition.isLoading && <Loader2 className='animate-spin' />}
      {competition.error && (
        <Alert variant='destructive'>
          <AlertDescription>{errorMessage(competition.error)}</AlertDescription>
        </Alert>
      )}
      {data && (
        <Card className='mb-6'>
          <CardContent className='grid gap-6 sm:grid-cols-3 lg:grid-cols-6'>
            <Fact label='Status' value={<StatusBadge status={data.status} />} />
            <Fact
              label='Players'
              value={`${data.entryCount} / ${data.maxEntries}`}
            />
            <Fact
              label='Entry'
              value={paid ? money(data.entryFeeMinor!, data.currency) : 'Free'}
            />
            <Fact
              label='Registration closes'
              value={dateTime(data.registrationClosesAt)}
            />
            <Fact label='Starts' value={dateTime(data.startsAt)} />
            <Fact label='Matches' value={data.matchCount ?? 0} />
          </CardContent>
        </Card>
      )}

      <h2 className='mb-3 text-lg font-bold'>Registrations</h2>
      <div className='rounded-md border'>
        <Table>
          <TableHeader>
            <TableRow>
              <TableHead>Player</TableHead>
              <TableHead>In-game name</TableHead>
              <TableHead>Entry</TableHead>
              {paid && <TableHead>Payment</TableHead>}
              <TableHead>Checked in</TableHead>
              <TableHead>Registered</TableHead>
            </TableRow>
          </TableHeader>
          <TableBody>
            <ListRows
              isLoading={entries.isLoading}
              error={entries.error}
              empty='No registrations yet.'
              columns={paid ? 6 : 5}
              count={entries.items.length}
            />
            {entries.items.map((entry) => (
              <TableRow key={entry.id}>
                <TableCell>
                  <div className='font-medium'>{entry.playerName}</div>
                  {entry.handle && (
                    <div className='text-xs text-muted-foreground'>
                      @{entry.handle}
                    </div>
                  )}
                </TableCell>
                <TableCell className='text-sm'>
                  {entry.inGameName || '—'}
                </TableCell>
                <TableCell>
                  <StatusBadge status={entry.status} />
                  {entry.seed && (
                    <div className='mt-1 text-xs text-muted-foreground'>
                      Seed {entry.seed}
                    </div>
                  )}
                </TableCell>
                {paid && (
                  <TableCell>
                    {entry.paymentStatus ? (
                      <>
                        <StatusBadge status={entry.paymentStatus} />
                        <div className='mt-1 font-mono text-xs text-muted-foreground'>
                          {money(data!.entryFeeMinor!, data!.currency)}
                        </div>
                      </>
                    ) : (
                      '—'
                    )}
                  </TableCell>
                )}
                <TableCell className='text-sm text-muted-foreground'>
                  {dateTime(entry.checkedInAt)}
                </TableCell>
                <TableCell className='text-sm text-muted-foreground'>
                  {dateTime(entry.createdAt)}
                </TableCell>
              </TableRow>
            ))}
          </TableBody>
        </Table>
      </div>
      <LoadMore
        hasNextPage={entries.hasNextPage}
        isFetchingNextPage={entries.isFetchingNextPage}
        fetchNextPage={entries.fetchNextPage}
      />
    </Page>
  )
}
