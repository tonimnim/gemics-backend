import { createFileRoute } from '@tanstack/react-router'
import { StrikesPage } from '@/features/strikes'

export const Route = createFileRoute('/_authenticated/strikes/')({
  component: StrikesPage,
})
