import { createFileRoute } from '@tanstack/react-router'
import { PlayersPage } from '@/features/players'

export const Route = createFileRoute('/_authenticated/players/')({
  component: PlayersPage,
})
