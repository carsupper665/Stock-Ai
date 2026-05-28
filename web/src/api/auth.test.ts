import { describe, expect, it, vi } from 'vitest';

import { ApiClient, ApiError } from './client';
import { loginAdmin } from './auth';

describe('loginAdmin', () => {
  it('posts admin credentials to the backend session endpoint', async () => {
    const fetchMock = vi.fn<typeof fetch>().mockResolvedValueOnce(
      jsonResponse({ id: 7, username: 'operator', role: 'admin' }),
    );
    const client = new ApiClient({ baseUrl: 'https://api.example.test', fetchImpl: fetchMock });

    const user = await loginAdmin({ username: 'operator', password: 'secret' }, client);

    expect(user).toEqual({ id: 7, username: 'operator', role: 'admin' });
    expect(fetchMock).toHaveBeenCalledWith(
      'https://api.example.test/admin/login',
      expect.objectContaining({
        method: 'POST',
        credentials: 'include',
        body: JSON.stringify({ username: 'operator', password: 'secret' }),
        headers: { 'Content-Type': 'application/json' },
      }),
    );
  });

  it('surfaces backend login failures as ApiError', async () => {
    const fetchMock = vi.fn<typeof fetch>().mockResolvedValueOnce(
      jsonResponse({ code: 'UNAUTHORIZED', details: null, message: 'admin login required' }, 401),
    );
    const client = new ApiClient({ baseUrl: 'https://api.example.test', fetchImpl: fetchMock });

    await expect(loginAdmin({ username: 'operator', password: 'wrong' }, client)).rejects.toMatchObject({
      status: 401,
      code: 'UNAUTHORIZED',
      message: 'admin login required',
    } satisfies Partial<ApiError>);
  });
});

function jsonResponse(body: unknown, status = 200): Response {
  return new Response(JSON.stringify(body), {
    status,
    headers: { 'Content-Type': 'application/json' },
  });
}
