import { useInfiniteQuery } from '@tanstack/react-query'
import { api } from '@/lib/api'

export type CursorPage<T> = {
  data: T[]
  page: { nextCursor: string | null; hasMore: boolean }
}

/** A cursor-paged admin list, loaded a page at a time with "Load more". */
export function usePagedList<T>(
  key: unknown[],
  path: string,
  query: Record<string, string | number | undefined>,
  enabled = true
) {
  const {
    data,
    isLoading,
    error,
    hasNextPage,
    isFetchingNextPage,
    fetchNextPage,
  } = useInfiniteQuery({
    queryKey: [...key, path, query],
    enabled,
    initialPageParam: undefined as string | undefined,
    queryFn: ({ pageParam }) =>
      api<CursorPage<T>>(path, {
        query: { limit: 50, ...query, cursor: pageParam },
      }),
    getNextPageParam: (last) =>
      last.page.hasMore ? (last.page.nextCursor ?? undefined) : undefined,
  })
  return {
    items: data?.pages.flatMap((page) => page.data) ?? [],
    isLoading,
    error,
    hasNextPage,
    isFetchingNextPage,
    fetchNextPage,
  }
}
