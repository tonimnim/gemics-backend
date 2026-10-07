import { createFileRoute } from '@tanstack/react-router'
import { CompetitionDetailPage } from '@/features/competitions/detail'

export const Route = createFileRoute('/_authenticated/competitions/$id')({
  component: function CompetitionRoute() {
    const { id } = Route.useParams()
    return <CompetitionDetailPage id={id} />
  },
})
