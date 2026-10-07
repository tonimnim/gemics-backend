import { Link } from '@tanstack/react-router'
import { Logo } from '@/assets/logo'
import { useLayout } from '@/context/layout-provider'
import {
  Sidebar,
  SidebarContent,
  SidebarFooter,
  SidebarHeader,
  SidebarRail,
  useSidebar,
} from '@/components/ui/sidebar'
import { Brand } from '@/components/brand'
import { useNavGroups } from './data/sidebar-data'
import { NavGroup } from './nav-group'
import { NavUser } from './nav-user'

export function AppSidebar() {
  const { collapsible, variant } = useLayout()
  const { setOpenMobile, state } = useSidebar()
  const navGroups = useNavGroups()
  return (
    <Sidebar collapsible={collapsible} variant={variant}>
      <SidebarHeader className='h-[4.5rem] justify-center px-4'>
        <Link
          to='/'
          onClick={() => setOpenMobile(false)}
          aria-label='Tonits home'
        >
          {state === 'collapsed' ? (
            <Logo className='size-8 text-primary' />
          ) : (
            <Brand subtitle='Staff dashboard' />
          )}
        </Link>
      </SidebarHeader>
      <SidebarContent className='px-2'>
        {navGroups.map((props) => (
          <NavGroup key={props.title} {...props} />
        ))}
      </SidebarContent>
      <SidebarFooter className='p-3'>
        <NavUser />
      </SidebarFooter>
      <SidebarRail />
    </Sidebar>
  )
}
