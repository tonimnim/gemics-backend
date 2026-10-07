import { useCan, type Permission } from '@/stores/auth-store'
import { Header } from '@/components/layout/header'
import { Main } from '@/components/layout/main'
import { ProfileDropdown } from '@/components/profile-dropdown'
import { Search } from '@/components/search'
import { ThemeSwitch } from '@/components/theme-switch'
import { ForbiddenError } from '@/features/errors/forbidden'

type PageProps = {
  title: string
  description?: React.ReactNode
  /** The page is shown only to staff holding this permission. */
  permission: Permission
  actions?: React.ReactNode
  children: React.ReactNode
}

/** The frame every dashboard page shares: top bar, title and permission gate. */
export function Page({
  title,
  description,
  permission,
  actions,
  children,
}: PageProps) {
  const allowed = useCan(permission)
  return (
    <>
      <Header fixed>
        <h1 className='truncate text-xl font-bold tracking-tight'>{title}</h1>
        <Search className='hidden sm:flex' />
        <div className='ms-auto flex items-center gap-2'>
          <ThemeSwitch />
          <ProfileDropdown />
        </div>
      </Header>
      {allowed ? (
        <Main className='px-4 py-6 sm:px-6'>
          {(description || actions) && (
            <div className='mb-6 flex flex-wrap items-center justify-between gap-4'>
              {description && (
                <p className='max-w-2xl text-sm text-muted-foreground'>
                  {description}
                </p>
              )}
              {actions && <div className='ms-auto flex gap-2'>{actions}</div>}
            </div>
          )}
          {children}
        </Main>
      ) : (
        <ForbiddenError />
      )}
    </>
  )
}
