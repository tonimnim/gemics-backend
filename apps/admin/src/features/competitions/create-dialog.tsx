import { useState } from 'react'
import { useMutation, useQueryClient } from '@tanstack/react-query'
import { Check, Globe2, Loader2 } from 'lucide-react'
import { toast } from 'sonner'
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
import { createCompetition, type CompetitionFormat } from './api'
import { countryFlag, countryName, scopeCountries } from './countries'

const empty = {
  name: '',
  description: '',
  format: 'single_elimination' as CompetitionFormat,
  maxEntries: '16',
  entryFeeKES: '0',
  prizeKES: '0',
  prizeFunding: 'none' as 'none' | 'organizer' | 'sponsor',
  countries: [] as string[],
  registrationOpensAt: '',
  registrationClosesAt: '',
  startsAt: '',
}

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
    setForm((current) => ({ ...current, ...patch }))
  const paid = Number(form.entryFeeKES) > 0
  const prized = Number(form.prizeKES) > 0
  // Paid entry is collected by M-Pesa, so the API only lets players in Kenya
  // join. The form says so instead of offering a choice it would refuse.
  const countries = paid ? ['KE'] : form.countries
  const toggleCountry = (code: string) =>
    set({
      countries: form.countries.includes(code)
        ? form.countries.filter((item) => item !== code)
        : [...form.countries, code],
    })

  const create = useMutation({
    mutationFn: () => {
      const prizeMinor = Math.round(Number(form.prizeKES) * 100)
      return createCompetition(orgId, {
        name: form.name.trim(),
        description: form.description.trim(),
        gameId: 'efootball-mobile',
        format: form.format,
        maxEntries: Number(form.maxEntries),
        entryFeeMinor: Math.round(Number(form.entryFeeKES) * 100),
        currency: 'KES',
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

          <Section title='Money'>
            <div className='grid gap-4 sm:grid-cols-2'>
              <div className='grid gap-2'>
                <Label htmlFor='fee'>Entry fee (KES)</Label>
                <Input
                  id='fee'
                  type='number'
                  min={0}
                  step={1}
                  value={form.entryFeeKES}
                  onChange={(e) => set({ entryFeeKES: e.target.value })}
                />
              </div>
              <div className='grid gap-2'>
                <Label htmlFor='prize'>Prize (KES)</Label>
                <Input
                  id='prize'
                  type='number'
                  min={0}
                  step={1}
                  value={form.prizeKES}
                  onChange={(e) => set({ prizeKES: e.target.value })}
                />
              </div>
            </div>
            {prized && (
              <div className='grid gap-2 sm:w-1/2 sm:pe-2'>
                <Label>Prize funded by</Label>
                <Select
                  value={
                    form.prizeFunding === 'none'
                      ? 'organizer'
                      : form.prizeFunding
                  }
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
