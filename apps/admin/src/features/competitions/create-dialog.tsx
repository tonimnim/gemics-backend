import { useState } from 'react'
import { useMutation, useQueryClient } from '@tanstack/react-query'
import { Loader2 } from 'lucide-react'
import { toast } from 'sonner'
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
import { Textarea } from '@/components/ui/textarea'
import { createCompetition, type CompetitionFormat } from './api'

const empty = {
  name: '',
  description: '',
  format: 'single_elimination' as CompetitionFormat,
  maxEntries: '16',
  entryFeeKES: '0',
  prizeKES: '0',
  prizeFunding: 'none' as 'none' | 'organizer' | 'sponsor',
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
      })
    },
    onSuccess: () => {
      queryClient.invalidateQueries({ queryKey: ['competitions'] })
      toast.success('Draft created. Publish it when it is ready.')
      setForm(empty)
      onOpenChange(false)
    },
  })
  const paid = Number(form.entryFeeKES) > 0
  const prized = Number(form.prizeKES) > 0

  return (
    <Dialog open={open} onOpenChange={onOpenChange}>
      <DialogContent className='sm:max-w-lg'>
        <DialogHeader>
          <DialogTitle>New competition</DialogTitle>
          <DialogDescription>
            eFootball Mobile. It starts as a draft only staff can see.
          </DialogDescription>
        </DialogHeader>
        <form
          id='create-competition'
          className='grid max-h-[60vh] gap-4 overflow-y-auto pe-1'
          onSubmit={(event) => {
            event.preventDefault()
            create.mutate()
          }}
        >
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
              value={form.description}
              onChange={(e) => set({ description: e.target.value })}
            />
          </div>
          <div className='grid grid-cols-2 gap-4'>
            <div className='grid gap-2'>
              <Label>Format</Label>
              <Select
                value={form.format}
                onValueChange={(value) =>
                  set({ format: value as CompetitionFormat })
                }
              >
                <SelectTrigger>
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
          <div className='grid grid-cols-2 gap-4'>
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
              <p className='text-xs text-muted-foreground'>
                {paid
                  ? 'Paid by M-Pesa; open to players in Kenya only.'
                  : 'Free: open to every country.'}
              </p>
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
              {prized && (
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
                  <SelectTrigger>
                    <SelectValue />
                  </SelectTrigger>
                  <SelectContent>
                    <SelectItem value='organizer'>Funded by Tonits</SelectItem>
                    <SelectItem value='sponsor'>Funded by a sponsor</SelectItem>
                  </SelectContent>
                </Select>
              )}
            </div>
          </div>
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
                onChange={(e) => set({ registrationClosesAt: e.target.value })}
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
        </form>
        <DialogFooter>
          <Button
            type='submit'
            form='create-competition'
            disabled={create.isPending}
          >
            {create.isPending && <Loader2 className='animate-spin' />}
            Create draft
          </Button>
        </DialogFooter>
      </DialogContent>
    </Dialog>
  )
}
