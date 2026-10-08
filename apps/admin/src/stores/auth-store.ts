import { create } from 'zustand'
import { persist } from 'zustand/middleware'
import type { AuthSession } from '@/lib/api'

/** Every platform permission the API grants a staff role. */
export type Permission =
  | 'overview.view'
  | 'refund.view'
  | 'refund.manage'
  | 'game_account_verification.manage'
  | 'payment_review.manage'
  | 'result_review.manage'
  | 'player_strike.revoke'
  | 'competition.manage'
  | 'staff.manage'
  | 'finance.view'
  | 'player.view'
  | 'player.suspend'

export type StaffRole = 'support' | 'reviewer' | 'operator' | 'admin'

/** GET /v1/admin/me */
export type StaffSelf = {
  userId: string
  username: string | null
  displayName: string
  role: StaffRole
  permissions: Permission[]
  gamicsOrganizationId: string
}

type AuthState = {
  accessToken: string
  refreshToken: string
  staff: StaffSelf | null
  setSession: (session: AuthSession) => void
  setStaff: (staff: StaffSelf) => void
  reset: () => void
}

/**
 * Tokens persist in localStorage so a refresh keeps the staff member signed
 * in; the staff profile is re-read from the API on every page load so a
 * revoked role takes effect immediately.
 */
export const useAuthStore = create<AuthState>()(
  persist(
    (set) => ({
      accessToken: '',
      refreshToken: '',
      staff: null,
      setSession: (session) =>
        set({
          accessToken: session.accessToken,
          refreshToken: session.refreshToken,
        }),
      setStaff: (staff) => set({ staff }),
      reset: () => set({ accessToken: '', refreshToken: '', staff: null }),
    }),
    {
      name: 'tonits-admin-session',
      partialize: (state) => ({
        accessToken: state.accessToken,
        refreshToken: state.refreshToken,
      }),
    }
  )
)

const noPermissions: Permission[] = []

/**
 * The signed-in staff member's permissions. Selectors must return a stable
 * value, so "none" is one shared empty array rather than a new one per call.
 */
export function usePermissions(): Permission[] {
  return useAuthStore((state) => state.staff?.permissions ?? noPermissions)
}

export function useCan(permission: Permission): boolean {
  return useAuthStore(
    (state) => state.staff?.permissions.includes(permission) ?? false
  )
}

export const roleLabels: Record<StaffRole, string> = {
  support: 'Support',
  reviewer: 'Reviewer',
  operator: 'Operator',
  admin: 'Admin',
}
