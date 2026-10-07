import { useAuthStore } from '@/stores/auth-store'

/**
 * The Tonits API client. Every request carries the staff member's access
 * token; an expired token is refreshed once (refreshes are serialized, as
 * the API requires) and the request retried.
 */

export const API_URL = (
  import.meta.env.VITE_API_URL ?? 'http://localhost:8090'
).replace(/\/$/, '')

export class ApiError extends Error {
  readonly status: number
  readonly code: string
  readonly body: unknown

  constructor(status: number, code: string, message: string, body: unknown) {
    super(message)
    this.status = status
    this.code = code
    this.body = body
  }
}

type RequestOptions = {
  method?: 'GET' | 'POST' | 'PUT' | 'PATCH' | 'DELETE'
  body?: unknown
  query?: Record<string, string | number | boolean | null | undefined>
  /** Sends a fresh Idempotency-Key, which decision endpoints require. */
  idempotent?: boolean
  /** Skips the access token, for sign-in and refresh. */
  anonymous?: boolean
}

let refreshing: Promise<boolean> | null = null

export async function api<T>(
  path: string,
  options: RequestOptions = {}
): Promise<T> {
  const response = await send(path, options)
  if (response.status === 401 && !options.anonymous) {
    if (await refreshSession()) {
      return parse<T>(await send(path, options))
    }
    useAuthStore.getState().reset()
  }
  return parse<T>(response)
}

async function send(path: string, options: RequestOptions) {
  const url = new URL(API_URL + path)
  for (const [key, value] of Object.entries(options.query ?? {})) {
    if (value !== undefined && value !== null && value !== '') {
      url.searchParams.set(key, String(value))
    }
  }
  const headers: Record<string, string> = { Accept: 'application/json' }
  if (options.body !== undefined) headers['Content-Type'] = 'application/json'
  if (options.idempotent) headers['Idempotency-Key'] = crypto.randomUUID()
  const token = useAuthStore.getState().accessToken
  if (token && !options.anonymous) headers.Authorization = `Bearer ${token}`
  try {
    return await fetch(url, {
      method: options.method ?? 'GET',
      headers,
      body:
        options.body === undefined ? undefined : JSON.stringify(options.body),
    })
  } catch {
    throw new ApiError(0, 'network_error', 'Cannot reach the Tonits API.', null)
  }
}

async function parse<T>(response: Response): Promise<T> {
  if (response.status === 204) return undefined as T
  const text = await response.text()
  const body = text ? safeJSON(text) : null
  if (!response.ok) {
    const error = (body ?? {}) as { error?: string; message?: string }
    throw new ApiError(
      response.status,
      error.error ?? 'request_failed',
      error.message ?? `Request failed (${response.status}).`,
      body
    )
  }
  return body as T
}

function safeJSON(text: string): unknown {
  try {
    return JSON.parse(text)
  } catch {
    return null
  }
}

export type AuthSession = {
  accessToken: string
  refreshToken: string
  expiresInSeconds: number
}

/** Rotates the refresh token. Concurrent callers share one refresh. */
export function refreshSession(): Promise<boolean> {
  refreshing ??= (async () => {
    const refreshToken = useAuthStore.getState().refreshToken
    if (!refreshToken) return false
    try {
      const session = await parse<AuthSession>(
        await send('/v1/auth/refresh', {
          method: 'POST',
          body: { refreshToken },
          anonymous: true,
        })
      )
      useAuthStore.getState().setSession(session)
      return true
    } catch {
      return false
    } finally {
      refreshing = null
    }
  })()
  return refreshing
}

export function errorMessage(error: unknown): string {
  if (error instanceof ApiError) return error.message
  if (error instanceof Error) return error.message
  return 'Something went wrong.'
}
