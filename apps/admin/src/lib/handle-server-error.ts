import { toast } from 'sonner'
import { errorMessage } from '@/lib/api'

/** Shows a failed mutation's API message, which is written for staff. */
export function handleServerError(error: unknown) {
  if (import.meta.env.DEV) {
    // eslint-disable-next-line no-console
    console.error(error)
  }
  toast.error(errorMessage(error))
}
