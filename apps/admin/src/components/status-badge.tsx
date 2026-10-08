import { humanize } from '@/lib/format'
import { cn } from '@/lib/utils'
import { Badge } from '@/components/ui/badge'

const tones: Record<string, string> = {
  // Waiting on staff.
  queued: 'warn',
  requested: 'warn',
  under_review: 'warn',
  review: 'warn',
  manual_review: 'warn',
  pending: 'info',
  processing: 'info',
  initiating: 'info',
  callback_received: 'info',
  // Done well.
  approved: 'good',
  succeeded: 'good',
  decided: 'good',
  completed: 'good',
  active: 'good',
  verified: 'good',
  registration_open: 'good',
  running: 'good',
  check_in: 'info',
  published: 'info',
  // Done badly or stopped.
  rejected: 'bad',
  failed: 'bad',
  cancelled: 'bad',
  suspended: 'bad',
  revoked: 'muted',
  withdrawn: 'muted',
  closed: 'muted',
  draft: 'muted',
  deleted: 'muted',
  unverified: 'muted',
}

const classes: Record<string, string> = {
  warn: 'border-[#ff7448]/40 bg-[#ff7448]/15 text-[#c4421d] dark:text-[#ff9b78]',
  info: 'border-[#5677ff]/40 bg-[#5677ff]/15 text-[#3150d8] dark:text-[#9aaeff]',
  good: 'border-[#4ccb74]/40 bg-[#4ccb74]/15 text-[#23824a] dark:text-[#7ee0a0]',
  bad: 'border-destructive/40 bg-destructive/15 text-destructive',
  muted: 'border-border bg-muted text-muted-foreground',
}

export function StatusBadge({
  status,
  className,
}: {
  status: string
  className?: string
}) {
  return (
    <Badge
      variant='outline'
      className={cn(
        'font-mono text-[0.7rem]',
        classes[tones[status] ?? 'muted'],
        className
      )}
    >
      {humanize(status)}
    </Badge>
  )
}
