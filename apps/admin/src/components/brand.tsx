import { Logo } from '@/assets/logo'
import { cn } from '@/lib/utils'

/** The Tonits mark beside its wordmark, set in Alexandria ExtraBold. */
export function Brand({
  className,
  subtitle,
}: {
  className?: string
  subtitle?: string
}) {
  return (
    <div className={cn('flex items-center gap-2.5', className)}>
      <Logo className='size-8 shrink-0 text-primary' />
      <div className='grid leading-none'>
        <span className='font-brand text-lg font-extrabold tracking-tight text-foreground'>
          Tonits
        </span>
        {subtitle && (
          <span className='mt-1 text-[0.7rem] text-muted-foreground'>
            {subtitle}
          </span>
        )}
      </div>
    </div>
  )
}
