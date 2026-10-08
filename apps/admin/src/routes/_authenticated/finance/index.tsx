import { createFileRoute } from '@tanstack/react-router'
import { FinancePage } from '@/features/finance'

export const Route = createFileRoute('/_authenticated/finance/')({
  component: FinancePage,
})
