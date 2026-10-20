import { Loader2 } from 'lucide-react'
import { errorMessage } from '@/lib/api'
import { dateTime } from '@/lib/format'
import { cn } from '@/lib/utils'
import { Alert, AlertDescription } from '@/components/ui/alert'
import { Card, CardContent, CardHeader, CardTitle } from '@/components/ui/card'
import { chatMessageText, useMatchChat } from './api'

/**
 * A match chat as both players saw it. Reported messages are marked, and
 * opening the transcript is recorded in the audit log.
 */
export function MatchChatTranscript({
  matchId,
  highlightSeq,
}: {
  matchId: string
  highlightSeq?: number
}) {
  const { data: chat, isLoading, error } = useMatchChat(matchId)
  const reported = new Set(chat?.reports.map((report) => report.messageSeq))
  return (
    <Card>
      <CardHeader>
        <CardTitle className='flex items-center justify-between text-base'>
          <span>Match chat</span>
          {chat?.roomCode && (
            <span className='font-mono text-xs font-normal text-muted-foreground'>
              Room {chat.roomCode}
            </span>
          )}
        </CardTitle>
      </CardHeader>
      <CardContent className='space-y-3 text-sm'>
        {isLoading && <Loader2 className='size-4 animate-spin' />}
        {error && (
          <Alert variant='destructive'>
            <AlertDescription>{errorMessage(error)}</AlertDescription>
          </Alert>
        )}
        {chat && chat.messages.length === 0 && (
          <p className='text-muted-foreground'>
            No messages. The players didn&apos;t chat, or the chat was deleted.
          </p>
        )}
        {chat?.agreedStartAt && (
          <p className='text-xs text-muted-foreground'>
            Agreed start {dateTime(chat.agreedStartAt)}
          </p>
        )}
        <ol className='space-y-2'>
          {chat?.messages.map((message) => {
            const player = message.sender === 'home' ? chat.home : chat.away
            return (
              <li
                key={message.seq}
                className={cn(
                  'rounded-md border px-3 py-2',
                  message.sender === 'away' && 'ms-8',
                  message.sender === 'home' && 'me-8',
                  reported.has(message.seq) && 'border-[#ff7448]',
                  message.seq === highlightSeq && 'bg-muted'
                )}
              >
                <div className='flex items-center justify-between gap-2 text-xs text-muted-foreground'>
                  <span>
                    <span className='font-mono uppercase'>
                      {message.sender}
                    </span>{' '}
                    {player?.handle}
                  </span>
                  <span>{dateTime(message.sentAt)}</span>
                </div>
                <p className='mt-1 break-words whitespace-pre-wrap'>
                  {chatMessageText(message, dateTime)}
                </p>
                {(message.contactHidden || reported.has(message.seq)) && (
                  <p className='mt-1 text-xs text-muted-foreground'>
                    {message.contactHidden && 'Contact details hidden. '}
                    {reported.has(message.seq) && 'Reported.'}
                  </p>
                )}
              </li>
            )
          })}
        </ol>
      </CardContent>
    </Card>
  )
}
