import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';

import { ApiClient, ApiError, DEFAULT_API_BASE_URL } from './client';

describe('ApiClient', () => {
  let fetchMock: ReturnType<typeof vi.fn<typeof fetch>>;
  const originalFetch = globalThis.fetch;

  beforeEach(() => {
    fetchMock = vi.fn<typeof fetch>();
    globalThis.fetch = fetchMock;
  });

  afterEach(() => {
    globalThis.fetch = originalFetch;
    vi.unstubAllEnvs();
  });

  it('uses the backend default base URL when none is configured', async () => {
    vi.stubEnv('VITE_API_BASE_URL', '');
    fetchMock.mockResolvedValueOnce(jsonResponse({ status: 'ok' }));

    const client = new ApiClient();
    await client.request<{ status: string }>('/healthz');

    expect(fetchMock).toHaveBeenCalledWith(
      `${DEFAULT_API_BASE_URL}/healthz`,
      expect.objectContaining({ credentials: 'include' }),
    );
  });

  it('sends JSON admin session requests with credentials included', async () => {
    fetchMock.mockResolvedValueOnce(jsonResponse({ token: 'created' }));

    const client = new ApiClient({ baseUrl: 'https://api.example.test/root/' });
    await client.request<{ token: string }>('/admin/login', {
      method: 'POST',
      body: { password: 'secret', username: 'operator' },
    });

    const [, init] = fetchMock.mock.calls[0];

    expect(fetchMock.mock.calls[0][0]).toBe('https://api.example.test/root/admin/login');
    expect(init).toMatchObject({
      method: 'POST',
      credentials: 'include',
      body: JSON.stringify({ password: 'secret', username: 'operator' }),
      headers: { 'Content-Type': 'application/json' },
    });
  });

  it('does not set a JSON content type for FormData bodies', async () => {
    fetchMock.mockResolvedValueOnce(jsonResponse({ id: 'dataset-1' }));
    const form = new FormData();
    form.append('file', new Blob(['csv']), 'prices.csv');

    const client = new ApiClient({ baseUrl: 'https://api.example.test' });
    await client.request<{ id: string }>('/admin/replay-datasets/123/import', {
      method: 'POST',
      body: form,
    });

    const [, init] = fetchMock.mock.calls[0];

    expect(init?.body).toBe(form);
    expect(init?.headers).toEqual({});
  });

  it('normalizes backend errors and calls onUnauthorized for HTTP 401', async () => {
    const onUnauthorized = vi.fn();
    fetchMock.mockResolvedValueOnce(
      jsonResponse(
        { code: 'unauthorized', details: { route: '/admin/sandboxes' }, message: 'Admin session required' },
        401,
      ),
    );

    const client = new ApiClient({ baseUrl: 'https://api.example.test', onUnauthorized });
    let capturedError: unknown;

    try {
      await client.request('/admin/sandboxes');
    } catch (error) {
      capturedError = error;
    }

    expect(capturedError).toBeInstanceOf(ApiError);
    expect(capturedError).toMatchObject({
      status: 401,
      code: 'unauthorized',
      message: 'Admin session required',
      details: { route: '/admin/sandboxes' },
    });
    expect(onUnauthorized).toHaveBeenCalledTimes(1);
  });
});

function jsonResponse(body: unknown, status = 200): Response {
  return new Response(JSON.stringify(body), {
    status,
    headers: { 'Content-Type': 'application/json' },
  });
}
