import { createFileRoute } from '@tanstack/react-router'
import { VerificationsPage } from '@/features/verifications'

export const Route = createFileRoute('/_authenticated/verifications/')({
  component: VerificationsPage,
})
