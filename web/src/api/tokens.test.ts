import { describe, expect, it, vi } from 'vitest';

import { createToken, revokeToken, rotateToken } from './tokens';

describe('token API service', () => {
  it('creates a token and returns the one-time plaintext secret from the backend response', async () => {
    const request = vi.fn().mockResolvedValueOnce({
      id: 'token-1',
      account_id: 'account-1',
      scopes: ['market:read'],
      token: 'token-1.secret',
    });

    const token = await createToken(
      { accountId: 'account-1', name: 'Agent Token', scopes: ['market:read'], expiresAt: '2026-12-31T00:00:00Z' },
      { request },
    );

    expect(request).toHaveBeenCalledWith('/tokens', {
      method: 'POST',
      body: {
        account_id: 'account-1',
        name: 'Agent Token',
        scopes: ['market:read'],
        expires_at: '2026-12-31T00:00:00Z',
      },
    });
    expect(token).toEqual({ id: 'token-1', accountId: 'account-1', scopes: ['market:read'], token: 'token-1.secret' });
  });

  it('rotates a token using the dedicated endpoint', async () => {
    const request = vi.fn().mockResolvedValueOnce({
      id: 'token-1',
      account_id: 'account-1',
      scopes: ['trade:read'],
      token: 'token-1.rotated',
    });

    await expect(rotateToken('token-1', { request })).resolves.toEqual({
      id: 'token-1',
      accountId: 'account-1',
      scopes: ['trade:read'],
      token: 'token-1.rotated',
    });
    expect(request).toHaveBeenCalledWith('/tokens/token-1/rotate', { method: 'POST' });
  });

  it('revokes a token using DELETE without retaining a plaintext token', async () => {
    const request = vi.fn().mockResolvedValueOnce(undefined);

    await revokeToken('token-1', { request });

    expect(request).toHaveBeenCalledWith('/tokens/token-1', { method: 'DELETE' });
  });
});
