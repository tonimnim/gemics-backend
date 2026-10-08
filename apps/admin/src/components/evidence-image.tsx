import { useQuery } from '@tanstack/react-query'
import { ImageOff, Loader2 } from 'lucide-react'
import { api } from '@/lib/api'
import { cn } from '@/lib/utils'

type EvidenceAccess = { data: { downloadUrl: string; expiresAt: string } }

/**
 * A private screenshot. The API hands out a short-lived signed URL, so it is
 * fetched when shown and refreshed before it expires. "thumb" is a small
 * cropped preview; "full" shows the whole image so nothing is cut off.
 */
export function EvidenceImage({
  id,
  ready = true,
  size = 'card',
  className,
}: {
  id: string
  ready?: boolean
  size?: 'thumb' | 'card' | 'full'
  className?: string
}) {
  const { data, isLoading, error } = useQuery({
    queryKey: ['evidence', id],
    queryFn: () => api<EvidenceAccess>(`/v1/evidence/${id}`),
    enabled: ready,
    staleTime: 4 * 60 * 1000,
  })
  const frame = cn(
    'flex items-center justify-center overflow-hidden bg-muted',
    size === 'thumb' && 'size-12 rounded-lg',
    size === 'card' && 'aspect-[9/16] w-40 rounded-md border',
    size === 'full' && 'w-full rounded-xl bg-[#0f1020]',
    className
  )
  if (!ready)
    return (
      <div className={cn(frame, 'text-xs text-muted-foreground')}>
        {size === 'thumb' ? '…' : 'Processing…'}
      </div>
    )
  if (isLoading)
    return (
      <div className={cn(frame, size === 'full' && 'h-80')}>
        <Loader2 className='size-4 animate-spin text-muted-foreground' />
      </div>
    )
  if (error || !data)
    return (
      <div className={cn(frame, size === 'full' && 'h-80')}>
        <ImageOff className='size-4 text-muted-foreground' />
      </div>
    )
  return (
    <a
      href={data.data.downloadUrl}
      target='_blank'
      rel='noreferrer'
      onClick={(event) => event.stopPropagation()}
      className={frame}
    >
      <img
        src={data.data.downloadUrl}
        alt='Screenshot'
        className={cn(
          size === 'full'
            ? 'max-h-[70vh] w-full object-contain'
            : 'h-full w-full object-cover'
        )}
      />
    </a>
  )
}
