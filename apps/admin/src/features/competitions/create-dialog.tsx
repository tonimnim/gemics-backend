import { useState } from 'react'
import { useMutation, useQueryClient } from '@tanstack/react-query'
import { Check, Globe2, Loader2 } from 'lucide-react'
import { toast } from 'sonner'
import { countryFlag, countryName, scopeCountries } from '@/lib/countries'
import {
  competitionCurrency,
  paidEntryCurrency,
  toMinor,
  toUsdMinor,
  usd,
} from '@/lib/currency'
import { cn } from '@/lib/utils'
import { Button } from '@/components/ui/button'
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
  Sheet,
  SheetContent,
  SheetDescription,
  SheetFooter,
  SheetHeader,
  SheetTitle,
} from '@/components/ui/sheet'
import { Textarea } from '@/components/ui/textarea'
import { rateFor, useFxRates } from '@/features/finance/api'
import { createCompetition, type CompetitionFormat } from './api'

const empty = {
  name: '',
  description: '',
  format: 'single_elimination' as CompetitionFormat,
  maxEntries: '16',
  entryFee: '0',
  prize: '0',
  // Only sent with a prize; the select shows Tonits until staff pick.
  prizeFunding: 'organizer' as 'organizer' | 'sponsor',
  countries: [] as string[],
  registrationOpensAt: '',
  registrationClosesAt: '',
  startsAt: '',
}

// Paid entry is collected by M-Pesa, so the API only lets players in Kenya
// join. The form says so instead of offering a choice it would refuse.
const countriesOf = (form: typeof empty) =>
  Number(form.entryFee) > 0 ? ['KE'] : form.countries

// The API prices a competition in its one country's currency, or in USD.
const pricedIn = (form: typeof empty) => competitionCurrency(countriesOf(form))

/** Creates a draft competition run by Tonits. Publishing is a separate step. */
export function CreateCompetitionDialog({
  orgId,
  open,
  onOpenChange,
}: {
  orgId: string
  open: boolean
  onOpenChange: (open: boolean) => void
}) {
  const queryClient = useQueryClient()
  const [form, setForm] = useState(empty)
  const set = (patch: Partial<typeof empty>) =>
    setForm((current) => {
      const next = { ...current, ...patch }
      // A typed prize means nothing in another currency, so a change of
      // currency clears it rather than silently repricing it.
      if (pricedIn(next) !== pricedIn(current)) next.prize = '0'
      return next
    })
  const rates = useFxRates()
  const paid = Number(form.entryFee) > 0
  const prized = Number(form.prize) > 0
  const countries = countriesOf(form)
  const currency = pricedIn(form)
  const usdHint = (major: string, inCurrency: string) => {
    if (!Number(major) || inCurrency === 'USD') return null
    const value = toUsdMinor(
      toMinor(major, inCurrency),
      inCurrency,
      rateFor(rates.data, inCurrency)
    )
    return value == null ? null : `≈ ${usd(value)}`
  }
  const toggleCountry = (code: string) =>
    set({
      countries: form.countries.includes(code)
        ? form.countries.filter((item) => item !== code)
        : [...form.countries, code],
    })

  const create = useMutation({
    mutationFn: () => {
      const prizeMinor = toMinor(form.prize, currency)
      return createCompetition(orgId, {
        name: form.name.trim(),
        description: form.description.trim(),
        gameId: 'efootball-mobile',
        format: form.format,
        maxEntries: Number(form.maxEntries),
        entryFeeMinor: toMinor(form.entryFee, paidEntryCurrency),
        prizeAmountMinor: prizeMinor,
        prizeFunding: prizeMinor > 0 ? form.prizeFunding : 'none',
        registrationOpensAt: new Date(form.registrationOpensAt).toISOString(),
        registrationClosesAt: new Date(form.registrationClosesAt).toISOString(),
        startsAt: new Date(form.startsAt).toISOString(),
        rules:
          countries.length > 0
            ? { eligibility: { allowedCountries: countries } }
            : {},
      })
    },
    onSuccess: () => {
      queryClient.invalidateQueries({ queryKey: ['competitions'] })
      toast.success('Draft created. Publish it when it is ready.')
      setForm(empty)
      onOpenChange(false)
    },
  })

  return (
    <Sheet open={open} onOpenChange={onOpenChange}>
      <SheetContent side='left' className='w-full gap-0 p-0 sm:max-w-2xl'>
        <SheetHeader className='border-b px-8 py-6'>
          <SheetTitle className='text-xl'>New competition</SheetTitle>
          <SheetDescription>
            eFootball Mobile. Starts as a draft only staff can see.
          </SheetDescription>
        </SheetHeader>
        <form
          id='create-competition'
          className='flex-1 space-y-8 overflow-y-auto px-8 py-6'
          onSubmit={(event) => {
            event.preventDefault()
            create.mutate()
          }}
        >
          <Section title='Basics'>
            <div className='grid gap-2'>
              <Label htmlFor='name'>Name</Label>
              <Input
                id='name'
                value={form.name}
                onChange={(e) => set({ name: e.target.value })}
                minLength={3}
                required
              />
            </div>
            <div className='grid gap-2'>
              <Label htmlFor='description'>Description</Label>
              <Textarea
                id='description'
                rows={3}
                value={form.description}
                onChange={(e) => set({ description: e.target.value })}
              />
            </div>
            <div className='grid gap-4 sm:grid-cols-2'>
              <div className='grid gap-2'>
                <Label>Format</Label>
                <Select
                  value={form.format}
                  onValueChange={(value) =>
                    set({ format: value as CompetitionFormat })
                  }
                >
                  <SelectTrigger className='w-full'>
                    <SelectValue />
                  </SelectTrigger>
                  <SelectContent>
                    <SelectItem value='single_elimination'>
                      Single elimination
                    </SelectItem>
                    <SelectItem value='double_elimination'>
                      Double elimination
                    </SelectItem>
                    <SelectItem value='round_robin'>Round robin</SelectItem>
                  </SelectContent>
                </Select>
              </div>
              <div className='grid gap-2'>
                <Label htmlFor='max'>Max players</Label>
                <Input
                  id='max'
                  type='number'
                  min={2}
                  max={1024}
                  value={form.maxEntries}
                  onChange={(e) => set({ maxEntries: e.target.value })}
                  required
                />
              </div>
            </div>
          </Section>

          <Section
            title='Open to'
            hint={
              paid
                ? 'Paid entry uses M-Pesa, so only players in Kenya can join.'
                : countries.length === 0
                  ? 'Every country.'
                  : `Only players from ${countries.map(countryName).join(', ')}.`
            }
          >
            <div className='flex flex-wrap gap-2'>
              <CountryChip
                selected={countries.length === 0}
                disabled={paid}
                onClick={() => set({ countries: [] })}
              >
                <Globe2 className='size-4' /> Every country
              </CountryChip>
              {scopeCountries.map((code) => (
                <CountryChip
                  key={code}
                  selected={countries.includes(code)}
                  disabled={paid}
                  onClick={() => toggleCountry(code)}
                >
                  <span aria-hidden>{countryFlag(code)}</span>
                  {countryName(code)}
                </CountryChip>
              ))}
            </div>
          </Section>

          <Section
            title='Money'
            hint={
              currency === 'USD'
                ? 'Priced in USD, as it is open to more than one country.'
                : `Priced in ${currency}, the currency of ${countryName(countries[0])}.`
            }
          >
            <div className='grid gap-4 sm:grid-cols-2'>
              <div className='grid gap-2'>
                <Label htmlFor='fee'>Entry fee ({paidEntryCurrency})</Label>
                <MoneyInput
                  id='fee'
                  currency={paidEntryCurrency}
                  value={form.entryFee}
                  onChange={(value) => set({ entryFee: value })}
                />
                <p className='text-xs text-muted-foreground'>
                  {usdHint(form.entryFee, paidEntryCurrency) ??
                    (paid
                      ? 'Paid entry is limited to Kenya.'
                      : 'Free to enter. A fee limits entry to Kenya.')}
                </p>
              </div>
              <div className='grid gap-2'>
                <Label htmlFor='prize'>Prize ({currency})</Label>
                <MoneyInput
                  id='prize'
                  currency={currency}
                  value={form.prize}
                  onChange={(value) => set({ prize: value })}
                />
                <p className='text-xs text-muted-foreground'>
                  {usdHint(form.prize, currency) ?? '\u00a0'}
                </p>
              </div>
            </div>
            {prized && (
              <div className='grid gap-2 sm:w-1/2 sm:pe-2'>
                <Label>Prize funded by</Label>
                <Select
                  value={form.prizeFunding}
                  onValueChange={(value) =>
                    set({ prizeFunding: value as 'organizer' | 'sponsor' })
                  }
                >
                  <SelectTrigger className='w-full'>
                    <SelectValue />
                  </SelectTrigger>
                  <SelectContent>
                    <SelectItem value='organizer'>Tonits</SelectItem>
                    <SelectItem value='sponsor'>A sponsor</SelectItem>
                  </SelectContent>
                </Select>
              </div>
            )}
          </Section>

          <Section title='Schedule'>
            <div className='grid gap-4 sm:grid-cols-3'>
              <div className='grid gap-2'>
                <Label htmlFor='opens'>Registration opens</Label>
                <Input
                  id='opens'
                  type='datetime-local'
                  value={form.registrationOpensAt}
                  onChange={(e) => set({ registrationOpensAt: e.target.value })}
                  required
                />
              </div>
              <div className='grid gap-2'>
                <Label htmlFor='closes'>Registration closes</Label>
                <Input
                  id='closes'
                  type='datetime-local'
                  value={form.registrationClosesAt}
                  onChange={(e) =>
                    set({ registrationClosesAt: e.target.value })
                  }
                  required
                />
              </div>
              <div className='grid gap-2'>
                <Label htmlFor='starts'>Starts</Label>
                <Input
                  id='starts'
                  type='datetime-local'
                  value={form.startsAt}
                  onChange={(e) => set({ startsAt: e.target.value })}
                  required
                />
              </div>
            </div>
          </Section>
        </form>
        <SheetFooter className='flex-row justify-end gap-2 border-t px-8 py-4'>
          <Button variant='ghost' onClick={() => onOpenChange(false)}>
            Cancel
          </Button>
          <Button
            type='submit'
            form='create-competition'
            disabled={create.isPending}
          >
            {create.isPending && <Loader2 className='animate-spin' />}
            Create draft
          </Button>
        </SheetFooter>
      </SheetContent>
    </Sheet>
  )
}

function Section({
  title,
  hint,
  children,
}: {
  title: string
  hint?: string
  children: React.ReactNode
}) {
  return (
    <section className='space-y-4'>
      <div>
        <h3 className='text-sm font-semibold'>{title}</h3>
        {hint && <p className='text-xs text-muted-foreground'>{hint}</p>}
      </div>
      {children}
    </section>
  )
}

function CountryChip({
  selected,
  disabled,
  onClick,
  children,
}: {
  selected: boolean
  disabled?: boolean
  onClick: () => void
  children: React.ReactNode
}) {
  return (
    <button
      type='button'
      disabled={disabled}
      aria-pressed={selected}
      onClick={onClick}
      className={cn(
        'inline-flex h-9 items-center gap-2 rounded-full border px-3.5 text-sm transition-colors',
        'disabled:cursor-not-allowed disabled:opacity-60',
        selected
          ? 'border-primary bg-primary/10 font-medium text-primary'
          : 'bg-background hover:bg-muted'
      )}
    >
      {children}
      {selected && <Check className='size-3.5' />}
    </button>
  )
}

/** A whole-number amount in major units, with the currency beside it. */
function MoneyInput({
  id,
  currency,
  value,
  onChange,
}: {
  id: string
  currency: string
  value: string
  onChange: (value: string) => void
}) {
  return (
    <div className='relative'>
      <Input
        id={id}
        type='number'
        min={0}
        step={1}
        inputMode='numeric'
        value={value}
        onChange={(event) => onChange(event.target.value)}
        className='pe-14'
      />
      <span className='pointer-events-none absolute end-3 top-1/2 -translate-y-1/2 text-xs font-medium text-muted-foreground'>
        {currency}
      </span>
    </div>
  )
}
