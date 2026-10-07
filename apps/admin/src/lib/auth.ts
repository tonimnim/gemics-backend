import { useAuthStore, type StaffSelf } from '@/stores/auth-store'
import { api, ApiError, type AuthSession } from '@/lib/api'

/** Signs a staff member in with their Konami ID and password. */
export async function signIn(konamiId: string, password: string) {
  const session = await api<AuthSession>('/v1/auth/login', {
    method: 'POST',
    body: { konamiId, password, deviceName: 'Tonits Admin' },
    anonymous: true,
  })
  useAuthStore.getState().setSession(session)
  try {
    await loadStaff()
  } catch (error) {
    await signOut()
    throw error
  }
}

/**
 * Loads who is signed in and what they may do. A player without a staff role
 * gets a clear message rather than an empty dashboard.
 */
export async function loadStaff(): Promise<StaffSelf> {
  try {
    const staff = await api<StaffSelf>('/v1/admin/me')
    useAuthStore.getState().setStaff(staff)
    return staff
  } catch (error) {
    if (error instanceof ApiError && error.status === 403) {
      throw new ApiError(
        403,
        error.code,
        'This account is not Tonits staff. Ask an admin to grant you a role.',
        error.body
      )
    }
    throw error
  }
}

export async function signOut() {
  const { accessToken } = useAuthStore.getState()
  if (accessToken) {
    await api('/v1/auth/logout', { method: 'POST' }).catch(() => undefined)
  }
  useAuthStore.getState().reset()
}
