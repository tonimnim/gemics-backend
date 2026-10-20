import { useState } from 'react'
import { useMutation, useQueryClient } from '@tanstack/react-query'
import { toast } from 'sonner'
import { api } from '@/lib/api'
import { ago, dateTime, humanize } from '@/lib/format'
import { usePagedList } from '@/lib/paged'
import { Button } from '@/components/ui/button'
import {
  Sheet,
  SheetContent,
  SheetDescription,
  SheetHeader,
  SheetTitle,
} from '@/components/ui/sheet'
import {
  Table,
  TableBody,
  TableCell,
  TableHead,
  TableHeader,
  TableRow,
} from '@/components/ui/table'
import { DecisionDialog } from '@/components/decision-dialog'
import { ListRows, LoadMore } from '@/components/list-state'
import { Page } from '@/components/page'
import { StatusBadge } from '@/components/status-badge'
import { chatMessageText, type ChatReport } from '@/features/match-chat/api'
import { MatchChatTranscript } from '@/features/match-chat/transcript'

type Resolution = { report: ChatReport; outcome: 'dismissed' | 'actioned' }

export function ChatReportsPage() {
  const queryClient = useQueryClient()
  const [status, setStatus] = useState<'open' | 'resolved'>('open')
  const [viewing, setViewing] = useState<ChatReport | null>(null)
  const [resolving, setResolving] = useState<Resolution | null>(null)
  const list = usePagedList<ChatReport>(
    ['chat-reports'],
    '/v1/admin/chat-reports',
    {
      status,
    }
  )
  const resolve = useMutation({
    mutationFn: (note: string) =>
      api(`/v1/admin/chat-reports/${resolving!.report.id}/resolution`, {
        method: 'POST',
        body: { outcome: resolving!.outcome, note },
      }),
    onSuccess: () => {
      toast.success('Report resolved.')
      setResolving(null)
      queryClient.invalidateQueries({ queryKey: ['chat-reports'] })
      queryClient.invalidateQueries({ queryKey: ['overview'] })
    },
  })

  return (
    <Page
      title='Chat reports'
      permission='result_review.manage'
      description='Messages players reported in match chats. To act on one, suspend the player or record a strike from their page, then mark the report actioned.'
      actions={
        <div className='flex gap-2'>
          {(['open', 'resolved'] as const).map((value) => (
            <Button
              key={value}
              size='sm'
              variant={status === value ? 'default' : 'outline'}
              onClick={() => setStatus(value)}
            >
              {humanize(value)}
            </Button>
          ))}
        </div>
      }
    >
      <div className='rounded-md border'>
        <Table>
          <TableHeader>
            <TableRow>
              <TableHead>Message</TableHead>
              <TableHead>Reason</TableHead>
              <TableHead>Players</TableHead>
              <TableHead>Reported</TableHead>
              <TableHead className='text-end' />
            </TableRow>
          </TableHeader>
          <TableBody>
            <ListRows
              isLoading={list.isLoading}
              error={list.error}
              empty={
                status === 'open'
                  ? 'No open reports.'
                  : 'No resolved reports yet.'
              }
              columns={5}
              count={list.items.length}
            />
            {list.items.map((report) => (
              <TableRow key={report.id}>
                <TableCell className='max-w-sm'>
                  <div className='text-sm break-words'>
                    {chatMessageText(
                      {
                        kind: report.messageKind,
                        text: report.messageText,
                        quickReply: report.messageText,
                        roomCode: report.messageText,
                        proposedStartAt: null,
                        accepted: null,
                      },
                      dateTime
                    )}
                  </div>
                  <div className='text-xs text-muted-foreground'>
                    {report.competitionName}
                  </div>
                </TableCell>
                <TableCell>
                  <div className='text-sm'>{humanize(report.reason)}</div>
                  {report.note && (
                    <div className='max-w-xs text-xs text-muted-foreground'>
                      {report.note}
                    </div>
                  )}
                </TableCell>
                <TableCell className='text-sm'>
                  <div>{report.reported.handle}</div>
                  <div className='text-xs text-muted-foreground'>
                    reported by {report.reporter.handle}
                  </div>
                </TableCell>
                <TableCell className='text-sm text-muted-foreground'>
                  {ago(report.createdAt)}
                  {report.status !== 'open' && (
                    <div className='mt-1'>
                      <StatusBadge status={report.status} />
                    </div>
                  )}
                </TableCell>
                <TableCell className='space-x-2 text-end whitespace-nowrap'>
                  <Button
                    size='sm'
                    variant='outline'
                    onClick={() => setViewing(report)}
                  >
                    Read chat
                  </Button>
                  {report.status === 'open' && (
                    <>
                      <Button
                        size='sm'
                        variant='outline'
                        onClick={() =>
                          setResolving({ report, outcome: 'dismissed' })
                        }
                      >
                        Dismiss
                      </Button>
                      <Button
                        size='sm'
                        onClick={() =>
                          setResolving({ report, outcome: 'actioned' })
                        }
                      >
                        Actioned
                      </Button>
                    </>
                  )}
                </TableCell>
              </TableRow>
            ))}
          </TableBody>
        </Table>
      </div>
      <LoadMore
        hasNextPage={list.hasNextPage}
        isFetchingNextPage={list.isFetchingNextPage}
        fetchNextPage={list.fetchNextPage}
      />
      <Sheet
        open={!!viewing}
        onOpenChange={(open) => !open && setViewing(null)}
      >
        <SheetContent className='w-full overflow-y-auto sm:max-w-lg'>
          <SheetHeader>
            <SheetTitle>{viewing?.competitionName}</SheetTitle>
            <SheetDescription>
              Reading a chat is recorded in the audit log.
            </SheetDescription>
          </SheetHeader>
          {viewing && (
            <div className='px-4 pb-6'>
              <MatchChatTranscript
                matchId={viewing.matchId}
                highlightSeq={viewing.messageSeq}
              />
            </div>
          )}
        </SheetContent>
      </Sheet>
      <DecisionDialog
        option={
          resolving
            ? resolving.outcome === 'dismissed'
              ? {
                  label: 'Dismiss report',
                  noteLabel: 'Why is no action needed?',
                }
              : {
                  label: 'Mark actioned',
                  noteLabel: 'What did you do?',
                  noteRequired: true,
                }
            : null
        }
        pending={resolve.isPending}
        onCancel={() => setResolving(null)}
        onConfirm={({ note }) => resolve.mutate(note)}
      />
    </Page>
  )
}
