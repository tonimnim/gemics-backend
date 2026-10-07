import { LogOut, Moon, Sun } from 'lucide-react'
import { roleLabels, useAuthStore } from '@/stores/auth-store'
import { initials } from '@/lib/format'
import { useTheme } from '@/context/theme-provider'
import useDialogState from '@/hooks/use-dialog-state'
import { Avatar, AvatarFallback } from '@/components/ui/avatar'
import { useSidebar } from '@/components/ui/sidebar'
import { SignOutDialog } from '@/components/sign-out-dialog'

/** The signed-in staff member's card, with theme and sign-out. */
export function NavUser() {
  const { state } = useSidebar()
  const { theme, setTheme } = useTheme()
  const [open, setOpen] = useDialogState()
  const staff = useAuthStore((current) => current.staff)
  if (!staff) return null

  const avatar = (
    <Avatar className='size-9 rounded-xl'>
      <AvatarFallback className='rounded-xl bg-primary text-xs font-semibold text-primary-foreground'>
        {initials(staff.displayName)}
      </AvatarFallback>
    </Avatar>
  )

  return (
    <>
      {state === 'collapsed' ? (
        <button
          onClick={() => setOpen(true)}
          aria-label='Sign out'
          className='mx-auto'
        >
          {avatar}
        </button>
      ) : (
        <div className='rounded-2xl bg-muted p-3'>
          <div className='flex items-center gap-2.5'>
            {avatar}
            <div className='grid min-w-0 leading-tight'>
              <span className='truncate text-sm font-semibold'>
                {staff.displayName}
              </span>
              <span className='truncate text-xs text-muted-foreground'>
                {roleLabels[staff.role]}
              </span>
            </div>
          </div>
          <div className='mt-3 flex items-center justify-between text-xs text-muted-foreground'>
            <button
              className='flex items-center gap-1.5 hover:text-foreground'
              onClick={() => setTheme(theme === 'dark' ? 'light' : 'dark')}
            >
              {theme === 'dark' ? (
                <Sun className='size-3.5' />
              ) : (
                <Moon className='size-3.5' />
              )}
              {theme === 'dark' ? 'Light mode' : 'Dark mode'}
            </button>
            <button
              className='flex items-center gap-1.5 hover:text-foreground'
              onClick={() => setOpen(true)}
            >
              <LogOut className='size-3.5' />
              Log out
            </button>
          </div>
        </div>
      )}
      <SignOutDialog open={!!open} onOpenChange={setOpen} />
    </>
  )
}
