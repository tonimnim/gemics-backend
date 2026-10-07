import { createFileRoute } from '@tanstack/react-router'
import { ResultReviewDetailPage } from '@/features/result-reviews/detail'

export const Route = createFileRoute('/_authenticated/result-reviews/$id')({
  component: function ResultReviewRoute() {
    const { id } = Route.useParams()
    return <ResultReviewDetailPage id={id} />
  },
})
