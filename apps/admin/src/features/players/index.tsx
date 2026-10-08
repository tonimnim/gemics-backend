import { useEffect, useState } from 'react'
import { useMutation, useQuery, useQueryClient } from '@tanstack/react-query'
import { Link } from '@tanstack/react-router'
import { Copy, Loader2, Search, ShieldAlert, Trophy, Users } from 'lucide-react'
import { toast } from 'sonner'
import { useCan } from '@/stores/auth-store'
import { api, errorMessage } from '@/lib/api'
import { countryFlag, countryName } from '@/lib/countries'
import { ago, dateTime, humanize, initials } from '@/lib/format'
import { usePagedList } from '@/lib/paged'
import { cn } from '@/lib/utils'
import { Button } from '@/components/ui/button'
import {
  Sheet,
  SheetContent,
  SheetHeader,
  SheetTitle,
} from '@/components/ui/sheet'
import { DecisionDialog } from '@/components/decision-dialog'
import { LoadMore } from '@/components/list-state'
import { Page } from '@/components/page'
import { StatusBadge } from '@/components/status-badge'

/** A row of GET /v1/admin/players. */
type Player = {
  id: string
  username: string | null
  displayName: string
  konamiId: string | null
  inGameName: string | null
  verification: string | null
  countryCode: string
  status: 'pending' | 'active' | 'suspended' | 'deleted'
  staffRole: string | null
  hasEmail: boolean
  hasPhone: boolean
  activeStrikes: number
  competitions: number
  createdAt: string
}

/** GET /v1/admin/players/{id} */
type PlayerDetail = Player & {
  email: string | null
  emailVerified: boolean
  phone: string | null
  lastSeenAt: string | null
  totalStrikes: number
  suspension: { reason: string; at: string; by: string | null } | null
  competitionList: {
    competitionId: string
    name: string
    status: string
    entryStatus: string
    placement: number | null
    joinedAt: string
  }[]
}

/** Waits for typing to pause before searching the server. */
function useDebounced<T>(value: T, delay = 300) {
  const [debounced, setDebounced] = useState(value)
  useEffect(() => {
    const timer = setTimeout(() => setDebounced(value), delay)
    return () => clearTimeout(timer)
  }, [value, delay])
  return debounced
}

export function PlayersPage() {
  const [search, setSearch] = useState('')
  const [selected, setSelected] = useState<string | null>(null)
  const q = useDebounced(search.trim())
  const list = usePagedList<Player>(['players'], '/v1/admin/players', {
    q: q || undefined,
  })

  return (
    <Page title='Players' permission='player.view'>
      <section className='overflow-hidden rounded-2xl bg-card'>
        <div className='flex flex-wrap items-center justify-between gap-3 border-b px-5 py-4'>
          <div className='flex items-center gap-2'>
            <h2 className='font-semibold'>All players</h2>
            <span className='rounded-full bg-muted px-2 py-0.5 text-xs font-medium text-muted-foreground'>
              {list.items.length}
              {list.hasNextPage ? '+' : ''}
            </span>
          </div>
          <label className='relative block w-full sm:w-80'>
            <Search className='pointer-events-none absolute start-3.5 top-1/2 size-4 -translate-y-1/2 text-muted-foreground' />
            <input
              value={search}
              onChange={(event) => setSearch(event.target.value)}
              placeholder='Search username, in-game name or Konami ID'
              className='h-10 w-full rounded-xl border border-transparent bg-muted ps-10 pe-4 text-sm outline-none focus:border-primary focus:bg-card focus:ring-4 focus:ring-primary/15'
            />
          </label>
        </div>

        <div className='hidden grid-cols-[minmax(0,2fr)_minmax(0,1.3fr)_minmax(0,1fr)_7rem_6rem_8rem_7rem] gap-4 border-b px-5 py-2.5 text-xs font-medium text-muted-foreground @4xl/content:grid'>
          <span>Player</span>
          <span>Konami ID</span>
          <span>Country</span>
          <span>Account</span>
          <span>Played</span>
          <span>Status</span>
          <span>Joined</span>
        </div>

        {list.isLoading ? (
          <div className='grid h-48 place-items-center'>
            <Loader2 className='animate-spin text-muted-foreground' />
          </div>
        ) : list.error ? (
          <div className='p-6 text-sm text-destructive'>
            {errorMessage(list.error)}
          </div>
        ) : list.items.length === 0 ? (
          <div className='grid place-items-center gap-3 py-16 text-center'>
            <span className='grid size-14 place-items-center rounded-full bg-[#eef0fb] text-[#5b5bd6]'>
              <Users className='size-6' />
            </span>
            <div className='font-semibold'>
              {q ? 'No matches' : 'No players yet'}
            </div>
          </div>
        ) : (
          <ul className='divide-y divide-border'>
            {list.items.map((player) => (
              <li
                key={player.id}
                onClick={() => setSelected(player.id)}
                className='grid cursor-pointer grid-cols-[1fr_auto] items-center gap-x-4 gap-y-1 px-5 py-3.5 transition-colors hover:bg-muted/50 @4xl/content:grid-cols-[minmax(0,2fr)_minmax(0,1.3fr)_minmax(0,1fr)_7rem_6rem_8rem_7rem]'
              >
                <PlayerName player={player} />
                <span className='hidden truncate font-mono text-sm @4xl/content:block'>
                  {player.konamiId ?? '—'}
                </span>
                <span className='hidden truncate text-sm @4xl/content:block'>
                  <span aria-hidden>{countryFlag(player.countryCode)}</span>{' '}
                  {countryName(player.countryCode)}
                </span>
                <span className='hidden @4xl/content:block'>
                  {player.verification && (
                    <StatusBadge status={player.verification} />
                  )}
                </span>
                <span className='hidden items-center gap-3 text-sm @4xl/content:flex'>
                  <span className='inline-flex items-center gap-1'>
                    <Trophy className='size-3.5 text-muted-foreground' />
                    {player.competitions}
                  </span>
                  {player.activeStrikes > 0 && (
                    <span className='inline-flex items-center gap-1 text-destructive'>
                      <ShieldAlert className='size-3.5' />
                      {player.activeStrikes}
                    </span>
                  )}
                </span>
                <span className='flex flex-wrap items-center gap-1.5'>
                  <StatusBadge status={player.status} />
                  {player.staffRole && <StaffChip role={player.staffRole} />}
                </span>
                <span className='hidden text-sm text-muted-foreground @4xl/content:block'>
                  {ago(player.createdAt)}
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
      <PlayerSheet playerId={selected} onClose={() => setSelected(null)} />
    </Page>
  )
}

function Avatar({ name, size = 'md' }: { name: string; size?: 'md' | 'lg' }) {
  return (
    <span
      className={cn(
        'grid shrink-0 place-items-center rounded-full bg-[#eef0fb] font-semibold text-[#4b4cd1]',
        size === 'lg' ? 'size-14 text-lg' : 'size-10 text-sm'
      )}
    >
      {initials(name) || '?'}
    </span>
  )
}

function PlayerName({ player }: { player: Player }) {
  return (
    <div className='flex min-w-0 items-center gap-3'>
      <Avatar name={player.username ?? player.displayName} />
      <div className='min-w-0'>
        <div className='truncate font-semibold'>
          {player.username ?? player.displayName}
        </div>
        <div className='truncate text-sm text-muted-foreground'>
          {player.inGameName || player.displayName}
        </div>
      </div>
    </div>
  )
}

function StaffChip({ role }: { role: string }) {
  return (
    <span className='rounded-full bg-primary/10 px-2 py-0.5 text-[0.7rem] font-medium text-primary'>
      {humanize(role)}
    </span>
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

function PlayerSheet({
  playerId,
  onClose,
}: {
  playerId: string | null
  onClose: () => void
}) {
  const queryClient = useQueryClient()
  const canSuspend = useCan('player.suspend')
  const [changing, setChanging] = useState<'suspend' | 'reactivate' | null>(
    null
  )
  const detail = useQuery({
    queryKey: ['players', 'detail', playerId],
    enabled: !!playerId,
    queryFn: () =>
      api<{ data: PlayerDetail }>(`/v1/admin/players/${playerId}`).then(
        (response) => response.data
      ),
  })
  const change = useMutation({
    mutationFn: (reason: string) =>
      api<{ data: PlayerDetail }>(
        `/v1/admin/players/${playerId}/${changing === 'suspend' ? 'suspension' : 'reactivation'}`,
        { method: 'POST', body: { reason } }
      ),
    onSuccess: (response) => {
      toast.success(
        changing === 'suspend' ? 'Player suspended.' : 'Player reactivated.'
      )
      setChanging(null)
      queryClient.setQueryData(['players', 'detail', playerId], response.data)
      queryClient.invalidateQueries({
        queryKey: ['players'],
        predicate: (query) => query.queryKey[1] !== 'detail',
      })
    },
  })
  const player = detail.data

  return (
    <Sheet open={!!playerId} onOpenChange={(open) => !open && onClose()}>
      <SheetContent className='w-full gap-0 overflow-y-auto sm:max-w-md'>
        <SheetHeader className='sr-only'>
          <SheetTitle>Player</SheetTitle>
        </SheetHeader>
        {detail.isLoading ? (
          <div className='grid h-full place-items-center'>
            <Loader2 className='animate-spin text-muted-foreground' />
          </div>
        ) : detail.error ? (
          <div className='p-6 text-sm text-destructive'>
            {errorMessage(detail.error)}
          </div>
        ) : (
          player && (
            <div className='flex h-full flex-col'>
              <div className='bg-[linear-gradient(150deg,#eef0ff,#e3e7ff)] px-6 pt-10 pb-6 dark:bg-[linear-gradient(150deg,#1d1f45,#1a1c3d)]'>
                <div className='flex items-center gap-4'>
                  <Avatar
                    name={player.username ?? player.displayName}
                    size='lg'
                  />
                  <div className='min-w-0'>
                    <div className='truncate text-xl font-bold tracking-tight'>
                      {player.username ?? player.displayName}
                    </div>
                    <div className='truncate text-sm text-muted-foreground'>
                      {player.inGameName || player.displayName}
                    </div>
                  </div>
                </div>
                <div className='mt-4 flex flex-wrap items-center gap-1.5'>
                  <StatusBadge status={player.status} />
                  {player.staffRole && <StaffChip role={player.staffRole} />}
                </div>
              </div>

              <div className='grid gap-6 p-6'>
                {player.suspension && (
                  <div className='rounded-xl border border-destructive/30 bg-destructive/5 p-3 text-sm'>
                    <div className='font-medium text-destructive'>
                      Suspended {dateTime(player.suspension.at)}
                      {player.suspension.by && ` by ${player.suspension.by}`}
                    </div>
                    <div className='mt-1 text-muted-foreground'>
                      {player.suspension.reason}
                    </div>
                  </div>
                )}

                <div className='grid grid-cols-3 gap-2 text-center'>
                  <Stat label='Played' value={player.competitions} />
                  <Stat
                    label='Strikes'
                    value={player.activeStrikes}
                    tone={player.activeStrikes > 0 ? 'bad' : undefined}
                  />
                  <Stat label='Seen' value={ago(player.lastSeenAt)} />
                </div>

                <dl className='grid gap-3 text-sm'>
                  <Row label='Konami ID'>
                    {player.konamiId ? (
                      <CopyValue value={player.konamiId} />
                    ) : (
                      '—'
                    )}
                  </Row>
                  <Row label='Account'>
                    {player.verification ? (
                      <StatusBadge status={player.verification} />
                    ) : (
                      '—'
                    )}
                  </Row>
                  <Row label='Country'>
                    <span aria-hidden>{countryFlag(player.countryCode)}</span>{' '}
                    {countryName(player.countryCode)}
                  </Row>
                  <Row label='Email'>
                    {player.email ? (
                      <span className='truncate'>
                        {player.email}
                        {!player.emailVerified && (
                          <span className='text-muted-foreground'>
                            {' '}
                            · unverified
                          </span>
                        )}
                      </span>
                    ) : (
                      <span className='text-muted-foreground'>Not added</span>
                    )}
                  </Row>
                  <Row label='Phone'>
                    {player.phone ? (
                      <span className='font-mono'>{player.phone}</span>
                    ) : (
                      <span className='text-muted-foreground'>Not added</span>
                    )}
                  </Row>
                  <Row label='Joined'>{dateTime(player.createdAt)}</Row>
                </dl>

                <div className='grid gap-2'>
                  <h3 className='text-sm font-semibold'>Competitions</h3>
                  {player.competitionList.length === 0 ? (
                    <p className='text-sm text-muted-foreground'>
                      Not entered any yet.
                    </p>
                  ) : (
                    <ul className='divide-y divide-border rounded-xl border'>
                      {player.competitionList.map((item) => (
                        <li key={item.competitionId}>
                          <Link
                            to='/competitions/$id'
                            params={{ id: item.competitionId }}
                            className='flex items-center justify-between gap-3 px-3 py-2.5 hover:bg-muted/50'
                          >
                            <div className='min-w-0'>
                              <div className='truncate text-sm font-medium'>
                                {item.name}
                              </div>
                              <div className='text-xs text-muted-foreground'>
                                {humanize(item.entryStatus)}
                                {item.placement && ` · #${item.placement}`}
                              </div>
                            </div>
                            <StatusBadge status={item.status} />
                          </Link>
                        </li>
                      ))}
                    </ul>
                  )}
                </div>
              </div>

              {canSuspend &&
                !player.staffRole &&
                (player.status === 'active' ||
                  player.status === 'suspended') && (
                  <div className='mt-auto border-t p-6'>
                    {player.status === 'active' ? (
                      <Button
                        variant='ghost'
                        className='h-11 w-full rounded-xl text-destructive'
                        onClick={() => setChanging('suspend')}
                      >
                        Suspend player
                      </Button>
                    ) : (
                      <Button
                        className='h-11 w-full rounded-xl'
                        onClick={() => setChanging('reactivate')}
                      >
                        Reactivate player
                      </Button>
                    )}
                  </div>
                )}
            </div>
          )
        )}
      </SheetContent>
      <DecisionDialog
        option={
          changing === 'suspend'
            ? {
                label: 'Suspend player',
                destructive: true,
                warning:
                  'They are signed out everywhere and cannot sign in until reactivated.',
                noteLabel: 'Reason',
                noteRequired: true,
              }
            : changing === 'reactivate'
              ? {
                  label: 'Reactivate player',
                  noteLabel: 'Reason',
                  noteRequired: true,
                }
              : null
        }
        pending={change.isPending}
        onCancel={() => setChanging(null)}
        onConfirm={({ note }) => change.mutate(note)}
      />
    </Sheet>
  )
}

function Stat({
  label,
  value,
  tone,
}: {
  label: string
  value: React.ReactNode
  tone?: 'bad'
}) {
  return (
    <div className='rounded-xl bg-muted/60 px-2 py-3'>
      <div
        className={cn(
          'truncate text-lg font-bold',
          tone === 'bad' && 'text-destructive'
        )}
      >
        {value}
      </div>
      <div className='text-xs text-muted-foreground'>{label}</div>
    </div>
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
      <dt className='shrink-0 text-muted-foreground'>{label}</dt>
      <dd className='flex min-w-0 items-center justify-end'>{children}</dd>
    </div>
  )
}
