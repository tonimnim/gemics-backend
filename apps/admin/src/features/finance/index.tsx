import { useState } from 'react'
import { format, parseISO } from 'date-fns'
import { useMutation, useQueryClient } from '@tanstack/react-query'
import {
  AlertTriangle,
  ArrowDownLeft,
  ArrowUpRight,
  Loader2,
  Pencil,
  Trophy,
  Wallet,
} from 'lucide-react'
import { Area, AreaChart, ResponsiveContainer, Tooltip, XAxis } from 'recharts'
import { toast } from 'sonner'
import { useCan } from '@/stores/auth-store'
import { api, errorMessage } from '@/lib/api'
import { money, usd } from '@/lib/currency'
import { ago } from '@/lib/format'
import { cn } from '@/lib/utils'
import { Button } from '@/components/ui/button'
import {
  Dialog,
  DialogContent,
  DialogDescription,
  DialogFooter,
  DialogHeader,
  DialogTitle,
} from '@/components/ui/dialog'
import { Input } from '@/components/ui/input'
import { Label } from '@/components/ui/label'
import {
  Select,
  SelectContent,
  SelectItem,
  SelectTrigger,
  SelectValue,
} from '@/components/ui/select'
import { Page } from '@/components/page'
import {
  financePeriods,
  useFinance,
  type FinancePeriod,
  type FinanceReport,
  type FxRate,
} from './api'

const indigo = '#5b5bd6'

const plural = (count: number, noun: string) =>
  `${count} ${noun}${count === 1 ? '' : 's'}`
const coral = '#ff7448'

export function FinancePage() {
  const [days, setDays] = useState<FinancePeriod>(30)
  const report = useFinance(days)
  const data = report.data

  return (
    <Page
      title='Finance'
      permission='finance.view'
      actions={
        <Select
          value={String(days)}
          onValueChange={(value) => setDays(Number(value) as FinancePeriod)}
        >
          <SelectTrigger className='w-40 rounded-xl bg-card'>
            <SelectValue />
          </SelectTrigger>
          <SelectContent align='end'>
            {financePeriods.map((period) => (
              <SelectItem key={period} value={String(period)}>
                Last {period === 365 ? 'year' : `${period} days`}
              </SelectItem>
            ))}
          </SelectContent>
        </Select>
      }
    >
      {report.isLoading ? (
        <div className='grid h-64 place-items-center'>
          <Loader2 className='animate-spin text-muted-foreground' />
        </div>
      ) : report.error ? (
        <div className='rounded-2xl bg-card p-6 text-sm text-destructive'>
          {errorMessage(report.error)}
        </div>
      ) : (
        data && (
          <div className='grid gap-5'>
            <Warnings data={data} />
            <Totals data={data} />
            <div className='grid gap-5 @4xl/content:grid-cols-[minmax(0,1.6fr)_minmax(0,1fr)]'>
              <Flow data={data} />
              <Rates rates={data.rates} />
            </div>
            <div className='grid gap-5 @4xl/content:grid-cols-[minmax(0,1.6fr)_minmax(0,1fr)]'>
              <ByCurrency data={data} />
              <Prizes data={data} />
            </div>
            <Recent data={data} />
          </div>
        )
      )}
    </Page>
  )
}

function Card({
  title,
  action,
  children,
  className,
}: {
  title?: string
  action?: React.ReactNode
  children: React.ReactNode
  className?: string
}) {
  return (
    <section className={cn('rounded-2xl bg-card p-5', className)}>
      {title && (
        <div className='mb-4 flex items-center justify-between gap-3'>
          <h2 className='font-semibold'>{title}</h2>
          {action}
        </div>
      )}
      {children}
    </section>
  )
}

function Warnings({ data }: { data: FinanceReport }) {
  const stale = data.rates.filter((rate) => rate.stale || !rate.unitsPerUsd)
  if (data.totals.unconverted === 0 && stale.length === 0) return null
  return (
    <div className='flex items-start gap-3 rounded-2xl border border-[#ff7448]/30 bg-[#ff7448]/10 p-4 text-sm'>
      <AlertTriangle className='mt-0.5 size-4 shrink-0 text-[#c4421d]' />
      <div className='grid gap-0.5'>
        {data.totals.unconverted > 0 && (
          <span>
            {data.totals.unconverted} payment
            {data.totals.unconverted === 1 ? ' is' : 's are'} waiting for an
            exchange rate and not in the USD totals yet.
          </span>
        )}
        {stale.length > 0 && (
          <span className='text-muted-foreground'>
            No recent rate for {stale.map((rate) => rate.currency).join(', ')}.
          </span>
        )}
      </div>
    </div>
  )
}

function Totals({ data }: { data: FinanceReport }) {
  const tiles = [
    {
      label: 'Collected',
      value: usd(data.totals.collectedUsdMinor),
      detail: plural(data.totals.payments, 'payment'),
      icon: ArrowDownLeft,
      tone: 'bg-[#e6f7ec] text-[#23824a]',
    },
    {
      label: 'Refunded',
      value: usd(data.totals.refundedUsdMinor),
      detail: plural(data.totals.refunds, 'refund'),
      icon: ArrowUpRight,
      tone: 'bg-[#fff0ea] text-[#c4421d]',
    },
    {
      label: 'Net',
      value: usd(data.totals.netUsdMinor),
      detail: 'Collected less refunded',
      icon: Wallet,
      tone: 'bg-[#eef0fb] text-[#4b4cd1]',
    },
    {
      label: 'Prizes',
      value: usd(data.totals.prizesUsdMinor),
      detail: `${usd(data.totals.upcomingPrizesUsdMinor)} upcoming`,
      icon: Trophy,
      tone: 'bg-[#fff6dd] text-[#b07900]',
    },
  ]
  return (
    <div className='grid gap-4 sm:grid-cols-2 @4xl/content:grid-cols-4'>
      {tiles.map((tile) => (
        <div key={tile.label} className='rounded-2xl bg-card p-5'>
          <span
            className={cn(
              'grid size-10 place-items-center rounded-xl',
              tile.tone
            )}
          >
            <tile.icon className='size-5' />
          </span>
          <div className='mt-4 text-sm text-muted-foreground'>{tile.label}</div>
          <div className='mt-1 text-3xl font-bold tracking-tight'>
            {tile.value}
          </div>
          <div className='mt-1 text-xs text-muted-foreground'>
            {tile.detail}
          </div>
        </div>
      ))}
    </div>
  )
}

function FlowTooltip({
  active,
  payload,
  label,
}: {
  active?: boolean
  payload?: { dataKey: string; value: number }[]
  label?: string
}) {
  if (!active || !payload?.length || !label) return null
  const value = (key: string) =>
    payload.find((item) => item.dataKey === key)?.value ?? 0
  return (
    <div className='rounded-lg border bg-card px-2.5 py-1.5 text-xs shadow-sm'>
      <div className='text-muted-foreground'>
        {format(parseISO(label), 'EEE d MMM')}
      </div>
      <div className='font-semibold text-[#23824a]'>
        In {usd(value('collectedUsdMinor'))}
      </div>
      <div className='font-semibold text-[#c4421d]'>
        Out {usd(value('refundedUsdMinor'))}
      </div>
    </div>
  )
}

function Flow({ data }: { data: FinanceReport }) {
  return (
    <Card
      title='Money in and out'
      action={
        <div className='flex items-center gap-4 text-xs text-muted-foreground'>
          <span className='flex items-center gap-1.5'>
            <span className='size-2 rounded-full bg-[#5b5bd6]' /> In
          </span>
          <span className='flex items-center gap-1.5'>
            <span className='size-2 rounded-full bg-[#ff7448]' /> Refunded
          </span>
        </div>
      }
    >
      <div className='h-56'>
        <ResponsiveContainer>
          <AreaChart
            data={data.daily}
            margin={{ top: 8, right: 0, bottom: 0, left: 0 }}
          >
            <defs>
              <linearGradient id='finance-in' x1='0' y1='0' x2='0' y2='1'>
                <stop offset='0%' stopColor={indigo} stopOpacity={0.3} />
                <stop offset='100%' stopColor={indigo} stopOpacity={0} />
              </linearGradient>
              <linearGradient id='finance-out' x1='0' y1='0' x2='0' y2='1'>
                <stop offset='0%' stopColor={coral} stopOpacity={0.25} />
                <stop offset='100%' stopColor={coral} stopOpacity={0} />
              </linearGradient>
            </defs>
            <XAxis
              dataKey='date'
              tickLine={false}
              axisLine={false}
              minTickGap={32}
              tick={{ fontSize: 11, fill: '#858aa3' }}
              tickFormatter={(value: string) =>
                format(parseISO(value), 'd MMM')
              }
            />
            <Tooltip cursor={false} content={<FlowTooltip />} />
            <Area
              type='monotone'
              dataKey='collectedUsdMinor'
              stroke={indigo}
              strokeWidth={2.5}
              fill='url(#finance-in)'
            />
            <Area
              type='monotone'
              dataKey='refundedUsdMinor'
              stroke={coral}
              strokeWidth={2}
              fill='url(#finance-out)'
            />
          </AreaChart>
        </ResponsiveContainer>
      </div>
    </Card>
  )
}

function ByCurrency({ data }: { data: FinanceReport }) {
  return (
    <Card title='By currency'>
      {data.byCurrency.length === 0 ? (
        <p className='py-8 text-center text-sm text-muted-foreground'>
          No money moved in this period.
        </p>
      ) : (
        <div className='overflow-x-auto'>
          <table className='w-full text-sm'>
            <thead className='text-xs text-muted-foreground'>
              <tr className='text-start'>
                <th className='pb-2 text-start font-medium'>Currency</th>
                <th className='pb-2 text-end font-medium'>Collected</th>
                <th className='pb-2 text-end font-medium'>Refunded</th>
                <th className='pb-2 text-end font-medium'>Net</th>
                <th className='pb-2 text-end font-medium'>In USD</th>
              </tr>
            </thead>
            <tbody className='divide-y divide-border'>
              {data.byCurrency.map((row) => (
                <tr key={row.currency}>
                  <td className='py-3 font-semibold'>{row.currency}</td>
                  <td className='py-3 text-end font-mono'>
                    {money(row.collectedMinor, row.currency)}
                  </td>
                  <td className='py-3 text-end font-mono text-muted-foreground'>
                    {money(row.refundedMinor, row.currency)}
                  </td>
                  <td className='py-3 text-end font-mono'>
                    {money(row.netMinor, row.currency)}
                  </td>
                  <td className='py-3 text-end font-mono font-semibold'>
                    {usd(row.collectedUsdMinor - row.refundedUsdMinor)}
                  </td>
                </tr>
              ))}
            </tbody>
          </table>
        </div>
      )}
    </Card>
  )
}

function Prizes({ data }: { data: FinanceReport }) {
  return (
    <Card title='Prizes'>
      {data.prizes.length === 0 ? (
        <p className='py-8 text-center text-sm text-muted-foreground'>
          No funded prizes.
        </p>
      ) : (
        <ul className='grid gap-3'>
          {data.prizes.map((prize) => (
            <li
              key={prize.currency}
              className='flex items-center justify-between gap-3 rounded-xl bg-muted/60 px-4 py-3'
            >
              <div>
                <div className='font-semibold'>{prize.currency}</div>
                <div className='text-xs text-muted-foreground'>
                  {prize.competitions} competition
                  {prize.competitions === 1 ? '' : 's'}
                </div>
              </div>
              <div className='text-end text-sm'>
                {prize.prizesMinor > 0 && (
                  <div className='font-mono'>
                    {money(prize.prizesMinor, prize.currency)}
                    <span className='ms-1.5 text-xs text-muted-foreground'>
                      {usd(prize.prizesUsdMinor)}
                    </span>
                  </div>
                )}
                {prize.upcomingMinor > 0 && (
                  <div className='font-mono text-muted-foreground'>
                    {money(prize.upcomingMinor, prize.currency)} upcoming
                    <span className='ms-1.5 text-xs'>
                      {usd(prize.upcomingUsdMinor)}
                    </span>
                  </div>
                )}
              </div>
            </li>
          ))}
        </ul>
      )}
      <p className='mt-3 text-xs text-muted-foreground'>
        Prizes are valued at today&apos;s rate.
      </p>
    </Card>
  )
}

function Rates({ rates }: { rates: FxRate[] }) {
  const canEdit = useCan('finance.manage')
  const [editing, setEditing] = useState<FxRate | null>(null)
  return (
    <Card title='Exchange rates'>
      <ul className='grid gap-1'>
        {rates.map((rate) => (
          <li
            key={rate.currency}
            className='group flex items-center gap-3 rounded-lg px-2 py-1.5 hover:bg-muted/60'
          >
            <span className='w-10 font-semibold'>{rate.currency}</span>
            <span className='flex-1 font-mono text-sm'>
              {rate.unitsPerUsd ? (
                <>
                  <span className='text-muted-foreground'>$1 = </span>
                  {Number(rate.unitsPerUsd).toLocaleString('en-US', {
                    maximumFractionDigits: 4,
                  })}
                </>
              ) : (
                <span className='text-muted-foreground'>No rate</span>
              )}
            </span>
            <span
              className={cn(
                'text-xs',
                rate.stale ? 'text-[#c4421d]' : 'text-muted-foreground'
              )}
              title={rate.source ?? undefined}
            >
              {rate.source === 'manual'
                ? 'Manual'
                : rate.rateDate
                  ? format(parseISO(rate.rateDate), 'd MMM')
                  : ''}
            </span>
            {canEdit && (
              <button
                type='button'
                onClick={() => setEditing(rate)}
                className='rounded-md p-1 text-muted-foreground opacity-0 group-hover:opacity-100 hover:bg-accent focus:opacity-100'
                aria-label={`Set the ${rate.currency} rate`}
              >
                <Pencil className='size-3.5' />
              </button>
            )}
          </li>
        ))}
      </ul>
      {/* Keyed so each currency starts with an empty field. */}
      <RateDialog
        key={editing?.currency ?? 'closed'}
        rate={editing}
        onClose={() => setEditing(null)}
      />
    </Card>
  )
}

/** Lets an admin enter today's rate when the provider is down. */
function RateDialog({
  rate,
  onClose,
}: {
  rate: FxRate | null
  onClose: () => void
}) {
  const queryClient = useQueryClient()
  const [value, setValue] = useState('')
  const save = useMutation({
    // The currency travels with the request, so closing the dialog while it
    // is in flight cannot break the success handler.
    mutationFn: (currency: string) =>
      api(`/v1/admin/fx-rates/${currency}`, {
        method: 'PUT',
        body: { unitsPerUsd: value.trim() },
      }),
    onSuccess: (_, currency) => {
      toast.success(`${currency} rate saved.`)
      queryClient.invalidateQueries({ queryKey: ['finance'] })
      queryClient.invalidateQueries({ queryKey: ['fx-rates'] })
      onClose()
    },
  })
  return (
    <Dialog open={!!rate} onOpenChange={(open) => !open && onClose()}>
      <DialogContent className='sm:max-w-sm'>
        <DialogHeader>
          <DialogTitle>{rate?.currency} rate for today</DialogTitle>
          <DialogDescription>
            Payments already converted keep their rate.
          </DialogDescription>
        </DialogHeader>
        <form
          id='fx-rate'
          className='grid gap-2'
          onSubmit={(event) => {
            event.preventDefault()
            if (rate) save.mutate(rate.currency)
          }}
        >
          <Label htmlFor='units'>1 US dollar buys</Label>
          <div className='relative'>
            <Input
              id='units'
              inputMode='decimal'
              placeholder={rate?.unitsPerUsd ?? ''}
              value={value}
              onChange={(event) => setValue(event.target.value)}
              className='pe-14'
              required
            />
            <span className='pointer-events-none absolute end-3 top-1/2 -translate-y-1/2 text-xs font-medium text-muted-foreground'>
              {rate?.currency}
            </span>
          </div>
          {save.error && (
            <p className='text-sm text-destructive'>
              {errorMessage(save.error)}
            </p>
          )}
        </form>
        <DialogFooter>
          <Button variant='ghost' onClick={onClose}>
            Cancel
          </Button>
          <Button type='submit' form='fx-rate' disabled={save.isPending}>
            {save.isPending && <Loader2 className='animate-spin' />}
            Save rate
          </Button>
        </DialogFooter>
      </DialogContent>
    </Dialog>
  )
}

function Recent({ data }: { data: FinanceReport }) {
  return (
    <section className='overflow-hidden rounded-2xl bg-card'>
      <div className='border-b px-5 py-4'>
        <h2 className='font-semibold'>Recent money</h2>
      </div>
      {data.recent.length === 0 ? (
        <p className='py-10 text-center text-sm text-muted-foreground'>
          No money moved in this period.
        </p>
      ) : (
        <ul className='divide-y divide-border'>
          {data.recent.map((item) => {
            const incoming = item.kind === 'payment'
            return (
              <li
                key={item.kind + item.id}
                className='flex items-center gap-3 px-5 py-3'
              >
                <span
                  className={cn(
                    'grid size-9 shrink-0 place-items-center rounded-full',
                    incoming
                      ? 'bg-[#e6f7ec] text-[#23824a]'
                      : 'bg-[#fff0ea] text-[#c4421d]'
                  )}
                >
                  {incoming ? (
                    <ArrowDownLeft className='size-4' />
                  ) : (
                    <ArrowUpRight className='size-4' />
                  )}
                </span>
                <div className='min-w-0 flex-1'>
                  <div className='truncate font-medium'>{item.player}</div>
                  <div className='truncate text-sm text-muted-foreground'>
                    {incoming ? 'Entry fee' : 'Refund'} · {item.competition}
                  </div>
                </div>
                <div className='text-end'>
                  <div
                    className={cn(
                      'font-mono text-sm font-semibold',
                      !incoming && 'text-[#c4421d]'
                    )}
                  >
                    {incoming ? '' : '−'}
                    {money(item.amountMinor, item.currency)}
                  </div>
                  <div className='text-xs text-muted-foreground'>
                    {item.usdMinor == null ? 'No rate yet' : usd(item.usdMinor)}{' '}
                    · {ago(item.at)}
                  </div>
                </div>
              </li>
            )
          })}
        </ul>
      )}
    </section>
  )
}
