import { createFileRoute } from '@tanstack/react-router'
import { RefundsPage } from '@/features/refunds'

export const Route = createFileRoute('/_authenticated/refunds/')({
  component: RefundsPage,
})
