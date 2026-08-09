const apiURL = process.env.EXPO_PUBLIC_API_URL ?? 'http://10.0.2.2:8080';

export class APIError extends Error {
  constructor(public readonly status: number, message: string, public readonly requestId?: string) {
    super(message);
    this.name = 'APIError';
  }
}

export async function request<T>(path: string, init: RequestInit = {}): Promise<T> {
  const response = await fetch(`${apiURL}${path}`, {
    ...init,
    headers: { Accept: 'application/json', 'Content-Type': 'application/json', ...init.headers },
  });
  if (!response.ok) {
    throw new APIError(response.status, `Request failed with status ${response.status}`, response.headers.get('X-Request-ID') ?? undefined);
  }
  return response.json() as Promise<T>;
}

export type GameCatalogResponse = { data: Array<{ id: string; name: string; publisher: string; platforms: string[]; resultMode: string; officialIntegration: boolean }> };
export const listGames = (signal?: AbortSignal) => request<GameCatalogResponse>('/v1/games', { signal });
