import { cn } from '@/lib/utils'
import { SidebarTrigger } from '@/components/ui/sidebar'

type HeaderProps = React.HTMLAttributes<HTMLElement> & {
  fixed?: boolean
  ref?: React.Ref<HTMLElement>
}

/**
 * The top bar: sidebar toggle, then whatever the page puts in it. Its look
 * never depends on scroll position, so it stays still while the page moves.
 */
export function Header({ className, fixed, children, ...props }: HeaderProps) {
  return (
    <header
      className={cn(
        'z-50 h-[4.5rem] shrink-0 border-b border-border bg-card',
        fixed && 'sticky top-0 w-[inherit]',
        className
      )}
      {...props}
    >
      <div className='flex h-full items-center gap-3 px-4 sm:gap-5 sm:px-6'>
        <SidebarTrigger variant='ghost' className='rounded-xl md:hidden' />
        {children}
      </div>
    </header>
  )
}
