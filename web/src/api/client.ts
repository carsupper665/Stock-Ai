type ImportMetaWithEnv = ImportMeta & {
  readonly env?: {
    readonly VITE_API_BASE_URL?: string;
  };
};

export const DEFAULT_API_BASE_URL = 'http://127.0.0.1:7794';

export type JsonObject = { readonly [key: string]: JsonValue };
export type JsonValue = JsonObject | readonly JsonValue[] | boolean | null | number | string;
export type ApiRequestBody = BodyInit | JsonObject | readonly JsonValue[];

export interface ApiClientOptions {
  readonly baseUrl?: string;
  readonly fetchImpl?: typeof fetch;
  readonly onUnauthorized?: () => void;
}

export interface ApiRequestOptions extends Omit<RequestInit, 'body'> {
  readonly body?: ApiRequestBody;
}

export class ApiError extends Error {
  readonly status: number;
  readonly code: string;
  readonly details: unknown;

  constructor(status: number, code: string, message: string, details?: unknown) {
    super(message);
    this.name = 'ApiError';
    this.status = status;
    this.code = code;
    this.details = details;
  }
}

export class ApiClient {
  private readonly baseUrl: string;
  private readonly fetchImpl: typeof fetch;
  private readonly onUnauthorized?: () => void;

  constructor(options: ApiClientOptions = {}) {
    this.baseUrl = resolveBaseUrl(options.baseUrl);
    this.fetchImpl = options.fetchImpl ?? globalThis.fetch.bind(globalThis);
    this.onUnauthorized = options.onUnauthorized;
  }

  async request<TResponse = unknown>(path: string, options: ApiRequestOptions = {}): Promise<TResponse> {
    const { body, headers, ...requestOptions } = options;
    const normalizedHeaders = toHeaderRecord(headers);
    const init: RequestInit = {
      ...requestOptions,
      credentials: requestOptions.credentials ?? 'include',
      headers: normalizedHeaders,
    };

    if (body !== undefined) {
      init.body = serializeBody(body, normalizedHeaders);
    }

    const response = await this.fetchImpl(this.toUrl(path), init);

    if (!response.ok) {
      if (response.status === 401) {
        this.onUnauthorized?.();
      }
      throw await toApiError(response);
    }

    return parseSuccessResponse<TResponse>(response);
  }

  private toUrl(path: string): string {
    if (this.baseUrl === '') {
      return path;
    }

    const baseUrl = this.baseUrl.replace(/\/+$/, '');
    const normalizedPath = path.startsWith('/') ? path : `/${path}`;
    return `${baseUrl}${normalizedPath}`;
  }
}

function resolveBaseUrl(explicitBaseUrl: string | undefined): string {
  if (explicitBaseUrl !== undefined) {
    return explicitBaseUrl.trim();
  }

  const configuredBaseUrl = (import.meta as ImportMetaWithEnv).env?.VITE_API_BASE_URL?.trim();
  return configuredBaseUrl && configuredBaseUrl.length > 0 ? configuredBaseUrl : DEFAULT_API_BASE_URL;
}

function serializeBody(body: ApiRequestBody, headers: Record<string, string>): BodyInit {
  if (isBodyInit(body)) {
    return body;
  }

  if (!hasContentType(headers)) {
    headers['Content-Type'] = 'application/json';
  }

  return JSON.stringify(body);
}

function toHeaderRecord(headers: HeadersInit | undefined): Record<string, string> {
  if (headers === undefined) {
    return {};
  }

  const headerRecord: Record<string, string> = {};
  new Headers(headers).forEach((value, key) => {
    headerRecord[key] = value;
  });
  return headerRecord;
}

function hasContentType(headers: Record<string, string>): boolean {
  return Object.keys(headers).some((headerName) => headerName.toLowerCase() === 'content-type');
}

function isBodyInit(body: ApiRequestBody): body is BodyInit {
  return (
    typeof body === 'string' ||
    isInstanceOf(body, FormData) ||
    isInstanceOf(body, URLSearchParams) ||
    isInstanceOf(body, Blob) ||
    isArrayBufferBody(body) ||
    isInstanceOf(body, ReadableStream)
  );
}

function isInstanceOf<T>(value: unknown, constructorValue: { new (...args: never[]): T } | undefined): value is T {
  return constructorValue !== undefined && value instanceof constructorValue;
}

function isArrayBufferBody(value: unknown): value is ArrayBuffer | ArrayBufferView {
  return value instanceof ArrayBuffer || ArrayBuffer.isView(value);
}

async function parseSuccessResponse<TResponse>(response: Response): Promise<TResponse> {
  if (response.status === 204) {
    return undefined as TResponse;
  }

  const text = await response.text();
  if (text === '') {
    return undefined as TResponse;
  }

  return JSON.parse(text) as TResponse;
}

async function toApiError(response: Response): Promise<ApiError> {
  const payload = await parseErrorPayload(response);

  return new ApiError(
    response.status,
    payload.code ?? `http_${response.status}`,
    payload.message ?? (response.statusText || `HTTP ${response.status}`),
    payload.details,
  );
}

async function parseErrorPayload(response: Response): Promise<Partial<{ code: string; details: unknown; message: string }>> {
  try {
    const payload: unknown = await response.json();
    if (!isRecord(payload)) {
      return {};
    }

    return {
      code: typeof payload.code === 'string' ? payload.code : undefined,
      message: typeof payload.message === 'string' ? payload.message : undefined,
      details: 'details' in payload ? payload.details : undefined,
    };
  } catch {
    return {};
  }
}

function isRecord(value: unknown): value is Record<string, unknown> {
  return typeof value === 'object' && value !== null;
}
