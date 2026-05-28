import { ApiClient } from './client';

export interface AdminLoginCredentials {
  readonly username: string;
  readonly password: string;
}

export interface AdminUser {
  readonly id: number | string;
  readonly username: string;
  readonly role: string;
}

type AuthClient = Pick<ApiClient, 'request'>;

const defaultClient = new ApiClient();

export function loginAdmin(
  credentials: AdminLoginCredentials,
  client: AuthClient = defaultClient,
): Promise<AdminUser> {
  return client.request<AdminUser>('/admin/login', {
    method: 'POST',
    body: {
      username: credentials.username,
      password: credentials.password,
    },
  });
}
