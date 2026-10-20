import { useQuery } from '@tanstack/react-query'
import { api } from '@/lib/api'

export type ChatMessage = {
  seq: number
  senderId: string
  sender: 'home' | 'away'
  kind: 'text' | 'quick_reply' | 'room_code' | 'time_proposal' | 'time_answer'
  text: string | null
  quickReply: string | null
  roomCode: string | null
  proposedStartAt: string | null
  replyToSeq: number | null
  accepted: boolean | null
  contactHidden: boolean
  sentAt: string
}

export type ChatPlayer = { userId: string; handle: string }

export type ChatReport = {
  id: string
  matchId: string
  competitionId: string
  competitionName: string
  messageSeq: number
  messageKind: ChatMessage['kind']
  messageText: string
  messageSentAt: string
  reporter: ChatPlayer
  reported: ChatPlayer
  reason: 'abuse' | 'harassment' | 'cheating' | 'spam' | 'other'
  note: string
  status: 'open' | 'dismissed' | 'actioned'
  resolutionNote: string
  resolvedBy: string | null
  resolvedAt: string | null
  createdAt: string
}

/** GET /v1/admin/match-chats/{matchId}; every read is audited. */
export type StaffMatchChat = {
  matchId: string
  competitionId: string
  competitionName: string
  state: string
  home: ChatPlayer | null
  away: ChatPlayer | null
  roomCode: string | null
  agreedStartAt: string | null
  messages: ChatMessage[]
  reports: ChatReport[]
}

export function useMatchChat(matchId: string, enabled = true) {
  return useQuery({
    queryKey: ['match-chat', matchId],
    queryFn: () =>
      api<{ data: StaffMatchChat }>(`/v1/admin/match-chats/${matchId}`).then(
        (response) => response.data
      ),
    enabled,
  })
}

const quickReplies: Record<string, string> = {
  ready_now: 'Ready now',
  five_minutes: 'Give me 5 minutes',
  ten_minutes: 'Give me 10 minutes',
  friend_request_sent: 'Friend request sent',
  room_created: 'Room created',
  good_game: 'Good game',
}

/** The words a player saw for a message, whatever its kind. */
export function chatMessageText(
  message: Pick<
    ChatMessage,
    'kind' | 'text' | 'quickReply' | 'roomCode' | 'proposedStartAt' | 'accepted'
  >,
  formatTime: (value: string) => string
) {
  switch (message.kind) {
    case 'text':
      return message.text ?? ''
    case 'quick_reply':
      return quickReplies[message.quickReply ?? ''] ?? message.quickReply ?? ''
    case 'room_code':
      return `Room code: ${message.roomCode}`
    case 'time_proposal':
      return `Proposed ${formatTime(message.proposedStartAt ?? '')}`
    case 'time_answer':
      return message.accepted ? 'Accepted the time' : 'Declined the time'
  }
}
