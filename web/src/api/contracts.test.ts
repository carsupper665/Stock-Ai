import { describe, expect, it } from 'vitest';

import { apiRouteContracts, findApiRouteContract } from './contracts';

const routeKey = (method: string, path: string) => `${method} ${path}`;

describe('apiRouteContracts', () => {
  it('documents the backend routes used by the frontend plan', () => {
    const expectedRoutes = [
      routeKey('GET', '/healthz'),
      routeKey('POST', '/admin/login'),
      routeKey('POST', '/auth/token/login'),
      routeKey('POST', '/admin/replay-datasets'),
      routeKey('GET', '/admin/replay-datasets'),
      routeKey('GET', '/admin/replay-datasets/:id'),
      routeKey('POST', '/admin/replay-datasets/:id/import'),
      routeKey('POST', '/admin/live-accounts'),
      routeKey('GET', '/admin/live-accounts'),
      routeKey('GET', '/admin/live-accounts/:id'),
      routeKey('PATCH', '/admin/live-accounts/:id'),
      routeKey('DELETE', '/admin/live-accounts/:id'),
      routeKey('POST', '/admin/sandboxes'),
      routeKey('GET', '/admin/sandboxes'),
      routeKey('GET', '/admin/sandboxes/:id'),
      routeKey('PATCH', '/admin/sandboxes/:id'),
      routeKey('DELETE', '/admin/sandboxes/:id'),
      routeKey('POST', '/admin/sandboxes/:id/start'),
      routeKey('POST', '/admin/sandboxes/:id/pause'),
      routeKey('POST', '/admin/sandboxes/:id/stop'),
      routeKey('POST', '/admin/sandboxes/:id/replay/seek'),
      routeKey('POST', '/admin/sandboxes/:id/replay/speed'),
      routeKey('POST', '/admin/sandboxes/:id/replay/resume'),
      routeKey('POST', '/admin/sandboxes/:id/accounts'),
      routeKey('GET', '/admin/sandboxes/:id/accounts'),
      routeKey('GET', '/admin/accounts/:id'),
      routeKey('PATCH', '/admin/accounts/:id'),
      routeKey('DELETE', '/admin/accounts/:id'),
      routeKey('GET', '/admin/monitor/sandboxes/:id/snapshot'),
      routeKey('GET', '/admin/monitor/live-symbols'),
      routeKey('POST', '/tokens'),
      routeKey('POST', '/tokens/:id/rotate'),
      routeKey('DELETE', '/tokens/:id'),
      routeKey('GET', '/ws/admin/monitor'),
      routeKey('GET', '/market/price'),
      routeKey('GET', '/market/ticker'),
      routeKey('GET', '/market/klines'),
      routeKey('GET', '/sandbox/time'),
      routeKey('POST', '/orders'),
      routeKey('GET', '/orders'),
      routeKey('GET', '/orders/:id'),
      routeKey('POST', '/orders/:id/cancel'),
      routeKey('GET', '/trades'),
      routeKey('GET', '/positions'),
      routeKey('GET', '/account'),
      routeKey('GET', '/account/performance'),
      routeKey('GET', '/ws/account'),
    ];

    expect(apiRouteContracts.map((route) => routeKey(route.method, route.path)).sort()).toEqual(
      expectedRoutes.sort(),
    );
  });

  it('records method, path, auth kind, and category metadata', () => {
    expect(findApiRouteContract('GET', '/healthz')).toMatchObject({
      method: 'GET',
      path: '/healthz',
      auth: 'none',
      category: 'health',
    });
    expect(findApiRouteContract('POST', '/admin/login')).toMatchObject({ auth: 'none', category: 'auth' });
    expect(findApiRouteContract('POST', '/admin/sandboxes/:id/start')).toMatchObject({
      auth: 'adminSession',
      category: 'admin',
    });
    expect(findApiRouteContract('GET', '/admin/monitor/live-symbols')).toMatchObject({
      auth: 'adminSession',
      category: 'monitor',
    });
    expect(findApiRouteContract('POST', '/tokens/:id/rotate')).toMatchObject({
      auth: 'adminSession',
      category: 'tokens',
    });
    expect(findApiRouteContract('GET', '/account/performance')).toMatchObject({
      auth: 'agentBearer',
      category: 'agent',
    });
  });

  it('keeps unsupported or invented endpoints out of the frontend contract map', () => {
    expect(apiRouteContracts.some((route) => route.path === '/admin/me')).toBe(false);
    expect(apiRouteContracts.some((route) => route.path === '/admin/logout')).toBe(false);
    expect(findApiRouteContract('PATCH', '/admin/replay-datasets/:id')).toBeUndefined();
    expect(findApiRouteContract('DELETE', '/admin/replay-datasets/:id')).toBeUndefined();
    expect(apiRouteContracts.some((route) => route.path === '/admin/profile')).toBe(false);
    expect(apiRouteContracts.some((route) => route.path === '/admin/billing')).toBe(false);
  });
});
