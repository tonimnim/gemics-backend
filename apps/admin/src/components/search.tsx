import { SearchIcon } from 'lucide-react'
import { cn } from '@/lib/utils'
import { useSearch } from '@/context/search-provider'
import { Button } from './ui/button'

export function Search({
  className = '',
  placeholder = 'Search',
  ...props
}: React.ComponentProps<'button'> & { placeholder?: string }) {
  const { setOpen } = useSearch()
  return (
    <Button
      {...props}
      variant='outline'
      className={cn(
        'group relative h-10 w-full flex-1 justify-start rounded-xl border-0 bg-muted text-sm font-normal text-muted-foreground shadow-none hover:bg-accent sm:w-56 sm:pe-12 md:flex-none lg:w-72',
        className
      )}
      aria-keyshortcuts='Meta+K Control+K'
      onClick={() => setOpen(true)}
    >
      <SearchIcon
        aria-hidden='true'
        className='absolute inset-s-3 top-1/2 -translate-y-1/2'
        size={16}
      />
      <span className='ms-6'>{placeholder}</span>
      <kbd className='pointer-events-none absolute inset-e-2 top-1/2 hidden h-5 -translate-y-1/2 items-center gap-1 rounded-md border bg-card px-1.5 font-mono text-[10px] font-medium opacity-100 select-none group-hover:bg-accent sm:flex'>
        <span className='text-xs'>⌘</span>K
      </kbd>
    </Button>
  )
}
