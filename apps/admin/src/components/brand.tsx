import { Wordmark } from '@/assets/wordmark'
import { cn } from '@/lib/utils'

/** The Ozone logo, with an optional line under it. */
export function Brand({
  className,
  subtitle,
}: {
  className?: string
  subtitle?: string
}) {
  return (
    <div className={cn('grid justify-items-start leading-none', className)}>
      <Wordmark className='h-6' />
      {subtitle && (
        <span className='mt-1.5 text-[0.7rem] text-muted-foreground'>
          {subtitle}
        </span>
      )}
    </div>
  )
}
