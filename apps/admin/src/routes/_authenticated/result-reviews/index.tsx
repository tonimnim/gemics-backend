import { createFileRoute } from '@tanstack/react-router'
import { ResultReviewsPage } from '@/features/result-reviews'

export const Route = createFileRoute('/_authenticated/result-reviews/')({
  component: ResultReviewsPage,
})
