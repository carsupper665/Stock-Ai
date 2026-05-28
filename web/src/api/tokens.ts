import { ApiClient, type JsonObject } from './client';

export interface CreateTokenInput {
  readonly accountId: string;
  readonly name: string;
  readonly scopes: readonly string[];
  readonly expiresAt?: string;
}

export interface TokenSecret {
  readonly id: string;
  readonly accountId: string;
  readonly scopes: readonly string[];
  readonly token: string;
}

interface TokenSecretResponse {
  readonly id: string;
  readonly account_id: string;
  readonly scopes: readonly string[];
  readonly token: string;
}

type TokenClient = Pick<ApiClient, 'request'>;

const defaultClient = new ApiClient();

export async function createToken(input: CreateTokenInput, client: TokenClient = defaultClient): Promise<TokenSecret> {
  const response = await client.request<TokenSecretResponse>('/tokens', {
    method: 'POST',
    body: createTokenBody(input),
  });
  return mapTokenSecret(response);
}

export async function rotateToken(tokenId: string, client: TokenClient = defaultClient): Promise<TokenSecret> {
  const response = await client.request<TokenSecretResponse>(`/tokens/${tokenId}/rotate`, { method: 'POST' });
  return mapTokenSecret(response);
}

export function revokeToken(tokenId: string, client: TokenClient = defaultClient): Promise<void> {
  return client.request<void>(`/tokens/${tokenId}`, { method: 'DELETE' });
}

function createTokenBody(input: CreateTokenInput): JsonObject {
  const body: Record<string, string | readonly string[]> = {
    account_id: input.accountId,
    name: input.name,
    scopes: input.scopes,
  };
  if (input.expiresAt !== undefined) {
    body.expires_at = input.expiresAt;
  }
  return body;
}

function mapTokenSecret(response: TokenSecretResponse): TokenSecret {
  return {
    id: response.id,
    accountId: response.account_id,
    scopes: response.scopes,
    token: response.token,
  };
}
