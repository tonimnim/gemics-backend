import { createFileRoute } from '@tanstack/react-router'
import { ChatReportsPage } from '@/features/chat-reports'

export const Route = createFileRoute('/_authenticated/chat-reports/')({
  component: ChatReportsPage,
})
