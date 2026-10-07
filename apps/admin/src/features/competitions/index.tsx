import { useState } from 'react'
import { useMutation, useQueryClient } from '@tanstack/react-query'
import { Link } from '@tanstack/react-router'
import { Loader2, MoreHorizontal, Plus, Shuffle } from 'lucide-react'
import { toast } from 'sonner'
import { useAuthStore } from '@/stores/auth-store'
import { dateTime, humanize, money } from '@/lib/format'
import { usePagedList } from '@/lib/paged'
import { Button } from '@/components/ui/button'
import {
  Dialog,
  DialogContent,
  DialogDescription,
  DialogFooter,
  DialogHeader,
  DialogTitle,
} from '@/components/ui/dialog'
import {
  DropdownMenu,
  DropdownMenuContent,
  DropdownMenuItem,
  DropdownMenuLabel,
  DropdownMenuSeparator,
  DropdownMenuTrigger,
} from '@/components/ui/dropdown-menu'
import { Input } from '@/components/ui/input'
import { Label } from '@/components/ui/label'
import {
  Select,
  SelectContent,
  SelectItem,
  SelectTrigger,
  SelectValue,
} from '@/components/ui/select'
import {
  Table,
  TableBody,
  TableCell,
  TableHead,
  TableHeader,
  TableRow,
} from '@/components/ui/table'
import { Textarea } from '@/components/ui/textarea'
import { ListRows, LoadMore } from '@/components/list-state'
import { Page } from '@/components/page'
import { StatusBadge } from '@/components/status-badge'
import {
  competitionsPath,
  drawCompetition,
  transitionCompetition,
  type Competition,
  type CompetitionStatus,
  type DrawRequest,
} from './api'
import { allowedCountries, countryFlag, countryName } from './countries'
import { CreateCompetitionDialog } from './create-dialog'

/** What staff call each lifecycle step. */
const transitionLabels: Record<CompetitionStatus, string> = {
  draft: 'Back to draft',
  published: 'Publish',
  registration_open: 'Open registration',
  check_in: 'Start check-in',
  running: 'Start competition',
  completed: 'Mark completed',
  cancelled: 'Cancel competition',
}

export function CompetitionsPage() {
  const orgId = useAuthStore((state) => state.staff?.gamicsOrganizationId ?? '')
  const queryClient = useQueryClient()
  const [creating, setCreating] = useState(false)
  const [moving, setMoving] = useState<{
    competition: Competition
    status: CompetitionStatus
  } | null>(null)
  const [reason, setReason] = useState('')
  const [drawing, setDrawing] = useState<Competition | null>(null)
  const [draw, setDraw] = useState<DrawRequest>({
    seedingPolicy: 'random',
    expectedStatus: 'check_in',
    config: { bestOf: 1 },
  })
  const list = usePagedList<Competition>(
    ['competitions'],
    competitionsPath(orgId),
    {},
    !!orgId
  )

  const refresh = () => {
    queryClient.invalidateQueries({ queryKey: ['competitions'] })
    queryClient.invalidateQueries({ queryKey: ['overview'] })
  }
  const transition = useMutation({
    mutationFn: () =>
      transitionCompetition(
        orgId,
        moving!.competition.id,
        moving!.status,
        reason.trim()
      ),
    onSuccess: ({ data }) => {
      toast.success(
        `${data.name} is now ${humanize(data.status).toLowerCase()}.`
      )
      setMoving(null)
      setReason('')
      refresh()
    },
  })
  const generate = useMutation({
    mutationFn: () => drawCompetition(orgId, drawing!.id, draw),
    onSuccess: () => {
      toast.success('Bracket generated. Start the competition when ready.')
      setDrawing(null)
      refresh()
    },
  })

  return (
    <Page
      title='Competitions'
      permission='competition.manage'
      actions={
        <Button onClick={() => setCreating(true)}>
          <Plus /> New competition
        </Button>
      }
    >
      <div className='rounded-md border'>
        <Table>
          <TableHeader>
            <TableRow>
              <TableHead>Competition</TableHead>
              <TableHead>Status</TableHead>
              <TableHead>Players</TableHead>
              <TableHead>Open to</TableHead>
              <TableHead>Entry</TableHead>
              <TableHead>Starts</TableHead>
              <TableHead className='w-12' />
            </TableRow>
          </TableHeader>
          <TableBody>
            <ListRows
              isLoading={list.isLoading}
              error={list.error}
              empty='No competitions here yet.'
              columns={7}
              count={list.items.length}
            />
            {list.items.map((competition) => (
              <TableRow key={competition.id}>
                <TableCell>
                  <Link
                    to='/competitions/$id'
                    params={{ id: competition.id }}
                    className='block hover:underline'
                  >
                    <div className='font-medium'>{competition.name}</div>
                    <div className='text-xs text-muted-foreground'>
                      {humanize(competition.format)}
                    </div>
                  </Link>
                </TableCell>
                <TableCell>
                  <StatusBadge status={competition.status} />
                </TableCell>
                <TableCell className='font-mono text-sm'>
                  {competition.entryCount}/{competition.maxEntries}
                </TableCell>
                <TableCell className='text-sm'>
                  <OpenTo competition={competition} />
                </TableCell>
                <TableCell className='text-sm'>
                  {competition.entryFeeMinor
                    ? money(competition.entryFeeMinor)
                    : 'Free'}
                </TableCell>
                <TableCell className='text-sm text-muted-foreground'>
                  {dateTime(competition.startsAt)}
                </TableCell>
                <TableCell>
                  <DropdownMenu>
                    <DropdownMenuTrigger asChild>
                      <Button variant='ghost' size='icon' aria-label='Actions'>
                        <MoreHorizontal />
                      </Button>
                    </DropdownMenuTrigger>
                    <DropdownMenuContent align='end'>
                      <DropdownMenuItem asChild>
                        <Link
                          to='/competitions/$id'
                          params={{ id: competition.id }}
                        >
                          View registrations
                        </Link>
                      </DropdownMenuItem>
                      <DropdownMenuSeparator />
                      <DropdownMenuLabel>Move to</DropdownMenuLabel>
                      {competition.allowedTransitions.length === 0 && (
                        <DropdownMenuItem disabled>
                          No further steps
                        </DropdownMenuItem>
                      )}
                      {competition.allowedTransitions.map((status) => (
                        <DropdownMenuItem
                          key={status}
                          variant={
                            status === 'cancelled' ? 'destructive' : 'default'
                          }
                          onClick={() => setMoving({ competition, status })}
                        >
                          {transitionLabels[status]}
                        </DropdownMenuItem>
                      ))}
                      {competition.status === 'check_in' && (
                        <>
                          <DropdownMenuSeparator />
                          <DropdownMenuItem
                            onClick={() => setDrawing(competition)}
                          >
                            <Shuffle /> Generate draw
                          </DropdownMenuItem>
                        </>
                      )}
                    </DropdownMenuContent>
                  </DropdownMenu>
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

      <CreateCompetitionDialog
        orgId={orgId}
        open={creating}
        onOpenChange={setCreating}
      />

      <Dialog open={!!moving} onOpenChange={(open) => !open && setMoving(null)}>
        <DialogContent className='sm:max-w-md'>
          <DialogHeader>
            <DialogTitle>
              {moving && transitionLabels[moving.status]}
            </DialogTitle>
            <DialogDescription>
              {moving?.status === 'cancelled'
                ? 'Cancelling ends every unfinished match and closes its reviews. Every paid entry gets a mandatory refund in the Refunds queue. This cannot be undone.'
                : `Move ${moving?.competition.name} to ${moving && humanize(moving.status).toLowerCase()}.`}
            </DialogDescription>
          </DialogHeader>
          <div className='grid gap-2'>
            <Label htmlFor='reason'>
              Reason {moving?.status === 'cancelled' ? '' : '(optional)'}
            </Label>
            <Textarea
              id='reason'
              value={reason}
              onChange={(e) => setReason(e.target.value)}
              maxLength={500}
            />
          </div>
          <DialogFooter>
            <Button
              variant={
                moving?.status === 'cancelled' ? 'destructive' : 'default'
              }
              disabled={
                transition.isPending ||
                (moving?.status === 'cancelled' && !reason.trim())
              }
              onClick={() => transition.mutate()}
            >
              {transition.isPending && <Loader2 className='animate-spin' />}
              Confirm
            </Button>
          </DialogFooter>
        </DialogContent>
      </Dialog>

      <Dialog
        open={!!drawing}
        onOpenChange={(open) => !open && setDrawing(null)}
      >
        <DialogContent className='sm:max-w-md'>
          <DialogHeader>
            <DialogTitle>Generate draw</DialogTitle>
            <DialogDescription>
              Builds the bracket for {drawing?.name} from players who have
              checked in.
            </DialogDescription>
          </DialogHeader>
          <div className='grid gap-4'>
            <div className='grid gap-2'>
              <Label>Seeding</Label>
              <Select
                value={draw.seedingPolicy}
                onValueChange={(value) =>
                  setDraw({
                    ...draw,
                    seedingPolicy: value as DrawRequest['seedingPolicy'],
                  })
                }
              >
                <SelectTrigger>
                  <SelectValue />
                </SelectTrigger>
                <SelectContent>
                  <SelectItem value='random'>Random</SelectItem>
                  <SelectItem value='rating'>By rating</SelectItem>
                  <SelectItem value='registration_order'>
                    Registration order
                  </SelectItem>
                  <SelectItem value='seeded'>Manual seeds</SelectItem>
                </SelectContent>
              </Select>
            </div>
            <div className='grid gap-2'>
              <Label htmlFor='best-of'>Best of (odd)</Label>
              <Input
                id='best-of'
                type='number'
                min={1}
                max={9}
                step={2}
                value={draw.config.bestOf}
                onChange={(e) =>
                  setDraw({
                    ...draw,
                    config: { ...draw.config, bestOf: Number(e.target.value) },
                  })
                }
              />
            </div>
          </div>
          <DialogFooter>
            <Button
              disabled={generate.isPending}
              onClick={() => generate.mutate()}
            >
              {generate.isPending && <Loader2 className='animate-spin' />}
              Generate
            </Button>
          </DialogFooter>
        </DialogContent>
      </Dialog>
    </Page>
  )
}

/** The countries a competition accepts players from. Paid entry is Kenya only. */
function OpenTo({ competition }: { competition: Competition }) {
  const countries = competition.entryFeeMinor
    ? ['KE']
    : allowedCountries(competition.rules)
  if (countries.length === 0) {
    return <span className='text-muted-foreground'>Every country</span>
  }
  return (
    <span className='inline-flex flex-wrap items-center gap-x-2'>
      {countries.map((code) => (
        <span key={code} className='inline-flex items-center gap-1'>
          <span aria-hidden>{countryFlag(code)}</span>
          {countryName(code)}
        </span>
      ))}
    </span>
  )
}
