import { useState } from 'react'
import { Loader2 } from 'lucide-react'
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
import { Textarea } from '@/components/ui/textarea'

export type DecisionOption = {
  label: string
  /** Shown before confirming, for consequences that cannot be undone. */
  warning?: string
  destructive?: boolean
  noteLabel?: string
  noteRequired?: boolean
  receiptRequired?: boolean
}

/** Confirms one staff decision, collecting the note or receipt it needs. */
export function DecisionDialog(props: {
  option: DecisionOption | null
  description?: React.ReactNode
  pending: boolean
  onCancel: () => void
  onConfirm: (input: { note: string; receipt: string }) => void
}) {
  return (
    <Dialog
      open={!!props.option}
      onOpenChange={(open) => !open && props.onCancel()}
    >
      <DialogContent className='sm:max-w-md'>
        {/* Keyed so every decision starts with an empty note. */}
        {props.option && (
          <DecisionForm
            key={props.option.label}
            {...props}
            option={props.option}
          />
        )}
      </DialogContent>
    </Dialog>
  )
}

function DecisionForm({
  option,
  description,
  pending,
  onConfirm,
}: {
  option: DecisionOption
  description?: React.ReactNode
  pending: boolean
  onConfirm: (input: { note: string; receipt: string }) => void
}) {
  const [note, setNote] = useState('')
  const [receipt, setReceipt] = useState('')
  const ready =
    (!option.noteRequired || note.trim()) &&
    (!option.receiptRequired || receipt.trim())
  return (
    <>
      <DialogHeader>
        <DialogTitle>{option.label}</DialogTitle>
        {(description || option.warning) && (
          <DialogDescription>
            {description}
            {option.warning && (
              <span className='mt-2 block font-medium text-destructive'>
                {option.warning}
              </span>
            )}
          </DialogDescription>
        )}
      </DialogHeader>
      <div className='grid gap-4'>
        {option.receiptRequired && (
          <div className='grid gap-2'>
            <Label htmlFor='receipt'>M-Pesa receipt</Label>
            <Input
              id='receipt'
              value={receipt}
              onChange={(e) => setReceipt(e.target.value)}
              maxLength={128}
              placeholder='e.g. SGR7XYZ123'
            />
          </div>
        )}
        <div className='grid gap-2'>
          <Label htmlFor='decision-note'>
            {option.noteLabel ?? 'Note'}{' '}
            {option.noteRequired ? '' : '(optional)'}
          </Label>
          <Textarea
            id='decision-note'
            value={note}
            onChange={(e) => setNote(e.target.value)}
            maxLength={1000}
          />
        </div>
      </div>
      <DialogFooter>
        <Button
          variant={option.destructive ? 'destructive' : 'default'}
          disabled={pending || !ready}
          onClick={() =>
            onConfirm({ note: note.trim(), receipt: receipt.trim() })
          }
        >
          {pending && <Loader2 className='animate-spin' />}
          Confirm
        </Button>
      </DialogFooter>
    </>
  )
}
