import { createFileRoute } from '@tanstack/react-router'
import { CompetitionsPage } from '@/features/competitions'

export const Route = createFileRoute('/_authenticated/competitions/')({
  component: CompetitionsPage,
})
