import { format } from 'date-fns'
import { useQuery } from '@tanstack/react-query'
import { Link } from '@tanstack/react-router'
import {
  BadgeCheck,
  Banknote,
  ChevronRight,
  Gavel,
  Loader2,
  ReceiptText,
} from 'lucide-react'
import {
  Area,
  AreaChart,
  Bar,
  BarChart,
  Cell,
  Pie,
  PieChart,
  ResponsiveContainer,
  Tooltip,
  XAxis,
} from 'recharts'
import {
  useAuthStore,
  useCan,
  usePermissions,
  type Permission,
} from '@/stores/auth-store'
import { api, errorMessage } from '@/lib/api'
import { dateTime, humanize, usd } from '@/lib/format'
import { cn } from '@/lib/utils'
import { Alert, AlertDescription } from '@/components/ui/alert'
import { Button } from '@/components/ui/button'
import { Page } from '@/components/page'
import { StatusBadge } from '@/components/status-badge'
import { competitionsPath, type Competition } from '@/features/competitions/api'
import { useOverview, type Overview } from './api'

const indigo = '#5b5bd6'
const lavender = '#c9caf7'

function Panel({
  title,
  action,
  className,
  children,
}: {
  title: string
  action?: React.ReactNode
  className?: string
  children: React.ReactNode
}) {
  return (
    <section
      className={cn(
        'rounded-2xl bg-card p-5 shadow-[0_1px_2px_rgba(27,29,58,0.04)]',
        className
      )}
    >
      <div className='mb-4 flex items-center justify-between gap-3'>
        <h2 className='text-base font-semibold'>{title}</h2>
        {action}
      </div>
      {children}
    </section>
  )
}

/** The lavender banner of headline numbers. */
function Highlights({ data }: { data: Overview }) {
  const tiles = [
    { value: data.players.total.toLocaleString(), label: 'Players', big: true },
    { value: `+${data.players.newLast7Days}`, label: 'New this week' },
    {
      value: data.competitions.registrationOpen,
      label: 'Open for registration',
    },
    { value: data.competitions.running, label: 'Running now' },
    data.finance
      ? {
          value: usd(data.finance.collectedMinorLast30Days),
          label: data.finance.unconvertedLast30Days
            ? `Collected, 30 days · ${data.finance.unconvertedLast30Days} awaiting a rate`
            : 'Collected, 30 days',
        }
      : { value: data.competitions.draft, label: 'Drafts' },
  ]
  return (
    <section className='relative overflow-hidden rounded-2xl bg-gradient-to-br from-[#e8ebff] via-[#e3e9ff] to-[#d9e6ff] p-6 dark:from-[#1d1f45] dark:via-[#1a1c3d] dark:to-[#16203f]'>
      <div className='pointer-events-none absolute -end-16 -top-24 size-80 rounded-full bg-[radial-gradient(circle,rgba(91,91,214,0.22)_1.5px,transparent_1.5px)] [background-size:9px_9px] opacity-70' />
      <div className='pointer-events-none absolute end-40 -bottom-28 size-64 rounded-full bg-[radial-gradient(circle,rgba(91,140,255,0.25)_1.5px,transparent_1.5px)] [background-size:9px_9px] opacity-60' />
      <div className='relative'>
        <h2 className='text-lg font-semibold'>Tonits at a glance</h2>
        <div className='mt-5 grid grid-cols-2 gap-3 md:grid-cols-3 xl:grid-cols-5'>
          {tiles.map((tile) => (
            <div
              key={tile.label}
              className='rounded-2xl border border-white/70 bg-white/65 p-4 backdrop-blur-sm dark:border-white/10 dark:bg-white/5'
            >
              <div
                className={cn(
                  'font-bold tracking-tight',
                  tile.big ? 'text-3xl' : 'text-2xl'
                )}
              >
                {tile.value}
              </div>
              <div className='mt-1 text-xs text-muted-foreground'>
                {tile.label}
              </div>
            </div>
          ))}
        </div>
      </div>
    </section>
  )
}

const statusGroups = [
  {
    label: 'Open',
    statuses: ['published', 'registration_open'],
    color: indigo,
  },
  { label: 'Live', statuses: ['check_in', 'running'], color: '#2ed3b7' },
  { label: 'Drafts', statuses: ['draft'], color: lavender },
  { label: 'Finished', statuses: ['completed', 'cancelled'], color: '#ffc84a' },
]

function CompetitionsDonut({ counts }: { counts: Record<string, number> }) {
  const slices = statusGroups.map((group) => ({
    ...group,
    value: group.statuses.reduce(
      (sum, status) => sum + (counts[status] ?? 0),
      0
    ),
  }))
  const total = slices.reduce((sum, slice) => sum + slice.value, 0)
  return (
    <div className='flex items-center gap-4'>
      <div className='relative size-36 shrink-0'>
        <ResponsiveContainer>
          <PieChart>
            <Pie
              data={
                total ? slices : [{ label: 'None', value: 1, color: '#eceef5' }]
              }
              dataKey='value'
              innerRadius='72%'
              outerRadius='100%'
              paddingAngle={total ? 3 : 0}
              cornerRadius={8}
              stroke='none'
            >
              {(total ? slices : [{ color: '#eceef5' }]).map((slice, index) => (
                <Cell key={index} fill={slice.color} />
              ))}
            </Pie>
          </PieChart>
        </ResponsiveContainer>
        <div className='absolute inset-0 grid place-items-center text-center'>
          <div>
            <div className='text-2xl font-bold'>{total}</div>
            <div className='text-[0.65rem] text-muted-foreground'>
              competitions
            </div>
          </div>
        </div>
      </div>
      <ul className='grid flex-1 gap-2.5 text-sm'>
        {slices.map((slice) => (
          <li key={slice.label} className='flex items-center gap-2'>
            <span
              className='size-2.5 rounded-full'
              style={{ background: slice.color }}
            />
            <span className='text-muted-foreground'>{slice.label}</span>
            <span className='ms-auto font-semibold'>{slice.value}</span>
          </li>
        ))}
      </ul>
    </div>
  )
}

function ChartTooltip({
  active,
  payload,
  label,
}: {
  active?: boolean
  payload?: { value: number }[]
  label?: string
}) {
  if (!active || !payload?.length || !label) return null
  return (
    <div className='rounded-lg border bg-card px-2.5 py-1.5 text-xs shadow-sm'>
      <div className='text-muted-foreground'>
        {format(new Date(label), 'EEE d MMM')}
      </div>
      <div className='font-semibold'>{payload[0].value}</div>
    </div>
  )
}

function NewPlayersBars({ days }: { days: Overview['playersByDay'] }) {
  const week = days.slice(-7)
  const total = week.reduce((sum, day) => sum + day.count, 0)
  return (
    <>
      <div className='mb-2 text-3xl font-bold'>{total}</div>
      <div className='h-32'>
        <ResponsiveContainer>
          <BarChart
            data={week}
            margin={{ top: 4, right: 0, bottom: 0, left: 0 }}
          >
            <XAxis
              dataKey='date'
              tickLine={false}
              axisLine={false}
              tick={{ fontSize: 11, fill: '#858aa3' }}
              tickFormatter={(value: string) =>
                format(new Date(value), 'EEEEE')
              }
            />
            <Tooltip cursor={false} content={<ChartTooltip />} />
            <Bar
              dataKey='count'
              radius={[999, 999, 999, 999]}
              barSize={14}
              minPointSize={6}
            >
              {week.map((day, index) => (
                <Cell
                  key={day.date}
                  fill={index === week.length - 1 ? indigo : lavender}
                />
              ))}
            </Bar>
          </BarChart>
        </ResponsiveContainer>
      </div>
    </>
  )
}

function RegistrationsWave({ days }: { days: Overview['registrationsByDay'] }) {
  const lastWeek = days.slice(-7).reduce((sum, day) => sum + day.count, 0)
  const weekBefore = days.slice(0, 7).reduce((sum, day) => sum + day.count, 0)
  return (
    <>
      <div className='h-32'>
        <ResponsiveContainer>
          <AreaChart
            data={days}
            margin={{ top: 8, right: 0, bottom: 0, left: 0 }}
          >
            <defs>
              <linearGradient
                id='registrations-fill'
                x1='0'
                y1='0'
                x2='0'
                y2='1'
              >
                <stop offset='0%' stopColor={indigo} stopOpacity={0.35} />
                <stop offset='100%' stopColor={indigo} stopOpacity={0} />
              </linearGradient>
            </defs>
            <XAxis dataKey='date' hide />
            <Tooltip cursor={false} content={<ChartTooltip />} />
            <Area
              type='monotone'
              dataKey='count'
              stroke={indigo}
              strokeWidth={2.5}
              fill='url(#registrations-fill)'
            />
          </AreaChart>
        </ResponsiveContainer>
      </div>
      <div className='mt-3 flex justify-center gap-8 text-sm'>
        <div className='text-center'>
          <div className='flex items-center gap-1.5 font-semibold'>
            <span
              className='size-2 rounded-full'
              style={{ background: indigo }}
            />
            {lastWeek}
          </div>
          <div className='text-xs text-muted-foreground'>This week</div>
        </div>
        <div className='text-center'>
          <div className='flex items-center gap-1.5 font-semibold'>
            <span
              className='size-2 rounded-full'
              style={{ background: lavender }}
            />
            {weekBefore}
          </div>
          <div className='text-xs text-muted-foreground'>Week before</div>
        </div>
      </div>
    </>
  )
}

type QueueItem = {
  label: string
  hint: string
  count: number
  to: string
  permission: Permission
  icon: React.ElementType
  tone: string
}

function NeedsAttention({ data }: { data: Overview }) {
  const permissions = usePermissions()
  const queues: QueueItem[] = [
    {
      label: 'Result reviews',
      hint: 'Scores that disagree',
      count: data.queues.resultReviews,
      to: '/result-reviews',
      permission: 'result_review.manage',
      icon: Gavel,
      tone: 'bg-[#ffe4e6] text-[#e5484d]',
    },
    {
      label: 'Account verifications',
      hint: 'Konami IDs to confirm',
      count: data.queues.accountVerifications,
      to: '/verifications',
      permission: 'game_account_verification.manage',
      icon: BadgeCheck,
      tone: 'bg-[#e0f7f2] text-[#12a594]',
    },
  ]
  if (data.finance) {
    queues.push(
      {
        label: 'Payment reviews',
        hint: 'M-Pesa outcomes',
        count: data.finance.paymentReviews,
        to: '/payments',
        permission: 'payment_review.manage',
        icon: Banknote,
        tone: 'bg-[#fff1d6] text-[#d98a00]',
      },
      {
        label: 'Refunds',
        hint: 'Waiting to be paid',
        count: data.finance.refunds,
        to: '/refunds',
        permission: 'refund.view',
        icon: ReceiptText,
        tone: 'bg-[#e8e9ff] text-[#5b5bd6]',
      }
    )
  }
  const items = queues.filter((item) => permissions.includes(item.permission))

  if (items.length === 0) {
    return (
      <p className='text-sm text-muted-foreground'>No queues for your role.</p>
    )
  }
  return (
    <ul className='grid gap-1'>
      {items.map((item) => (
        <li key={item.label}>
          <Link
            to={item.to}
            className='flex items-center gap-3 rounded-xl p-2 transition-colors hover:bg-muted'
          >
            <span
              className={cn(
                'grid size-10 place-items-center rounded-full',
                item.tone
              )}
            >
              <item.icon className='size-4.5' />
            </span>
            <span className='grid leading-tight'>
              <span className='text-sm font-semibold'>{item.label}</span>
              <span className='text-xs text-muted-foreground'>{item.hint}</span>
            </span>
            <span
              className={cn(
                'ms-auto text-sm font-bold',
                item.count > 0 ? 'text-foreground' : 'text-muted-foreground'
              )}
            >
              {item.count}
            </span>
            <ChevronRight className='size-4 text-muted-foreground' />
          </Link>
        </li>
      ))}
    </ul>
  )
}

function RecentCompetitions() {
  const orgId = useAuthStore((state) => state.staff?.gamicsOrganizationId ?? '')
  const allowed = useCan('competition.manage')
  const { data, isLoading } = useQuery({
    queryKey: ['competitions', 'recent', orgId],
    queryFn: () =>
      api<{ data: Competition[] }>(competitionsPath(orgId), {
        query: { limit: 5 },
      }),
    enabled: allowed && !!orgId,
  })
  if (!allowed)
    return (
      <p className='text-sm text-muted-foreground'>
        Your role does not manage competitions.
      </p>
    )
  if (isLoading) return <Loader2 className='animate-spin' />
  if (!data?.data.length) {
    return (
      <p className='text-sm text-muted-foreground'>
        No competitions yet. Create the first one.
      </p>
    )
  }
  return (
    <ul className='divide-y divide-border'>
      {data.data.map((competition) => (
        <li key={competition.id}>
          <Link
            to='/competitions/$id'
            params={{ id: competition.id }}
            className='flex items-center gap-4 py-3 hover:opacity-80'
          >
            <div className='min-w-0 flex-1'>
              <div className='truncate text-sm font-semibold'>
                {competition.name}
              </div>
              <div className='text-xs text-muted-foreground'>
                {humanize(competition.format)} · starts{' '}
                {dateTime(competition.startsAt)}
              </div>
            </div>
            <div className='hidden text-end text-sm sm:block'>
              <div className='font-semibold'>
                {competition.entryCount}/{competition.maxEntries}
              </div>
              <div className='text-xs text-muted-foreground'>players</div>
            </div>
            <StatusBadge status={competition.status} />
          </Link>
        </li>
      ))}
    </ul>
  )
}

export function OverviewPage() {
  const { data, isLoading, error } = useOverview()

  return (
    <Page title='Dashboard' permission='overview.view'>
      {isLoading && <Loader2 className='animate-spin' />}
      {error && (
        <Alert variant='destructive'>
          <AlertDescription>{errorMessage(error)}</AlertDescription>
        </Alert>
      )}
      {data && (
        <div className='grid gap-5'>
          <Highlights data={data} />
          <div className='grid gap-5 lg:grid-cols-3'>
            <Panel title='Competitions'>
              <CompetitionsDonut counts={data.competitionsByStatus} />
            </Panel>
            <Panel
              title='New players'
              action={
                <span className='text-xs text-muted-foreground'>
                  Last 7 days
                </span>
              }
            >
              <NewPlayersBars days={data.playersByDay} />
            </Panel>
            <Panel
              title='Registrations'
              action={
                <span className='text-xs text-muted-foreground'>
                  Last 14 days
                </span>
              }
            >
              <RegistrationsWave days={data.registrationsByDay} />
            </Panel>
          </div>
          <div className='grid gap-5 lg:grid-cols-3'>
            <Panel
              title='Recent competitions'
              className='lg:col-span-2'
              action={
                <Button variant='ghost' size='sm' asChild>
                  <Link to='/competitions'>View all</Link>
                </Button>
              }
            >
              <RecentCompetitions />
            </Panel>
            <Panel title='Needs attention'>
              <NeedsAttention data={data} />
            </Panel>
          </div>
        </div>
      )}
    </Page>
  )
}
