import type { ApiErrorShape } from '@/types/domain'

export class ApiError extends Error {
  status: number
  code: string
  details?: Record<string, unknown>

  constructor(status: number, payload: ApiErrorShape) {
    super(payload.message)
    this.name = 'ApiError'
    this.status = status
    this.code = payload.code
    this.details = payload.details
  }
}

let unauthorizedHandler: (() => void) | null = null

export function setUnauthorizedHandler(handler: (() => void) | null) {
  unauthorizedHandler = handler
}

function resolveBaseUrl() {
  const explicitBase = (import.meta.env.VITE_API_BASE_URL as string | undefined)?.replace(/\/$/, '')
  if (explicitBase) {
    return explicitBase
  }
  return import.meta.env.DEV ? '/__api' : ''
}

function buildUrl(path: string, query?: Record<string, string | number | boolean | undefined | null>) {
  const base = resolveBaseUrl()
  const url = new URL(`${base}${path}`, window.location.origin)
  if (query) {
    Object.entries(query).forEach(([key, value]) => {
      if (value === undefined || value === null || value === '') {
        return
      }
      url.searchParams.set(key, String(value))
    })
  }
  return base ? url.toString() : `${url.pathname}${url.search}`
}

async function parseResponse<T>(response: Response): Promise<T> {
  if (response.status === 204) {
    return undefined as T
  }

  const text = await response.text()
  const payload = text ? JSON.parse(text) : undefined

  if (!response.ok) {
    if (response.status === 401 && unauthorizedHandler) {
      unauthorizedHandler()
    }
    throw new ApiError(response.status, payload ?? { code: 'HTTP_ERROR', message: response.statusText })
  }

  return payload as T
}

async function request<T>(path: string, init: RequestInit = {}, query?: Record<string, string | number | boolean | undefined | null>) {
  const headers = new Headers(init.headers)
  if (!headers.has('Content-Type') && init.body && !(init.body instanceof FormData)) {
    headers.set('Content-Type', 'application/json')
  }

  return parseResponse<T>(await fetch(buildUrl(path, query), {
    ...init,
    headers,
    credentials: 'include',
  }))
}

export const apiClient = {
  get<T>(path: string, query?: Record<string, string | number | boolean | undefined | null>) {
    return request<T>(path, { method: 'GET' }, query)
  },
  post<T>(path: string, body?: unknown, query?: Record<string, string | number | boolean | undefined | null>) {
    return request<T>(path, { method: 'POST', body: body === undefined ? undefined : JSON.stringify(body) }, query)
  },
  patch<T>(path: string, body?: unknown, query?: Record<string, string | number | boolean | undefined | null>) {
    return request<T>(path, { method: 'PATCH', body: body === undefined ? undefined : JSON.stringify(body) }, query)
  },
  delete<T>(path: string, query?: Record<string, string | number | boolean | undefined | null>) {
    return request<T>(path, { method: 'DELETE' }, query)
  },
  upload<T>(path: string, formData: FormData) {
    return request<T>(path, { method: 'POST', body: formData })
  },
}
