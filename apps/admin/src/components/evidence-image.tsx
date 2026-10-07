import { useQuery } from '@tanstack/react-query'
import { ImageOff, Loader2 } from 'lucide-react'
import { api } from '@/lib/api'

type EvidenceAccess = { data: { downloadUrl: string; expiresAt: string } }

/**
 * A private screenshot. The API hands out a short-lived signed URL, so it is
 * fetched when shown and refreshed before it expires.
 */
export function EvidenceImage({
  id,
  ready = true,
}: {
  id: string
  ready?: boolean
}) {
  const { data, isLoading, error } = useQuery({
    queryKey: ['evidence', id],
    queryFn: () => api<EvidenceAccess>(`/v1/evidence/${id}`),
    enabled: ready,
    staleTime: 4 * 60 * 1000,
  })
  const frame =
    'flex aspect-[9/16] w-40 items-center justify-center overflow-hidden rounded-md border bg-muted'
  if (!ready)
    return (
      <div className={frame + ' text-xs text-muted-foreground'}>
        Processing…
      </div>
    )
  if (isLoading)
    return (
      <div className={frame}>
        <Loader2 className='animate-spin' />
      </div>
    )
  if (error || !data)
    return (
      <div className={frame}>
        <ImageOff className='text-muted-foreground' />
      </div>
    )
  return (
    <a
      href={data.data.downloadUrl}
      target='_blank'
      rel='noreferrer'
      className={frame}
    >
      <img
        src={data.data.downloadUrl}
        alt='Match screenshot'
        className='h-full w-full object-cover'
      />
    </a>
  )
}
