import { createFileRoute, redirect } from '@tanstack/react-router'
import { useAuthStore } from '@/stores/auth-store'
import { loadStaff } from '@/lib/auth'
import { AuthenticatedLayout } from '@/components/layout/authenticated-layout'

export const Route = createFileRoute('/_authenticated')({
  // Every dashboard page needs a signed-in staff member. The role is re-read
  // from the API on each load, so a revoked role stops working at once.
  beforeLoad: async ({ location }) => {
    const { refreshToken, staff } = useAuthStore.getState()
    if (!refreshToken) {
      throw redirect({ to: '/sign-in', search: { redirect: location.href } })
    }
    if (!staff) {
      try {
        await loadStaff()
      } catch {
        useAuthStore.getState().reset()
        throw redirect({ to: '/sign-in', search: { redirect: location.href } })
      }
    }
  },
  component: AuthenticatedLayout,
})
