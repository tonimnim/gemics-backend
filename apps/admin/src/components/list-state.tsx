import { Loader2 } from 'lucide-react'
import { errorMessage } from '@/lib/api'
import { Alert, AlertDescription } from '@/components/ui/alert'
import { Button } from '@/components/ui/button'
import { TableCell, TableRow } from '@/components/ui/table'

/** Loading, error and empty rows for a table, and its "Load more" footer. */
export function ListRows({
  isLoading,
  error,
  empty,
  columns,
  count,
}: {
  isLoading: boolean
  error: unknown
  empty: string
  columns: number
  count: number
}) {
  if (isLoading) {
    return (
      <TableRow>
        <TableCell
          colSpan={columns}
          className='h-24 text-center text-muted-foreground'
        >
          <Loader2 className='mx-auto animate-spin' />
        </TableCell>
      </TableRow>
    )
  }
  if (error) {
    return (
      <TableRow>
        <TableCell colSpan={columns}>
          <Alert variant='destructive'>
            <AlertDescription>{errorMessage(error)}</AlertDescription>
          </Alert>
        </TableCell>
      </TableRow>
    )
  }
  if (count === 0) {
    return (
      <TableRow>
        <TableCell
          colSpan={columns}
          className='h-24 text-center text-muted-foreground'
        >
          {empty}
        </TableCell>
      </TableRow>
    )
  }
  return null
}

export function LoadMore({
  hasNextPage,
  isFetchingNextPage,
  fetchNextPage,
}: {
  hasNextPage: boolean
  isFetchingNextPage: boolean
  fetchNextPage: () => unknown
}) {
  if (!hasNextPage) return null
  return (
    <div className='mt-4 flex justify-center'>
      <Button
        variant='outline'
        onClick={() => fetchNextPage()}
        disabled={isFetchingNextPage}
      >
        {isFetchingNextPage && <Loader2 className='animate-spin' />}
        Load more
      </Button>
    </div>
  )
}
