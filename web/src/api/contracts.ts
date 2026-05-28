export type ApiMethod = 'DELETE' | 'GET' | 'PATCH' | 'POST';
export type ApiAuthKind = 'adminSession' | 'agentBearer' | 'none';
export type ApiRouteCategory = 'admin' | 'agent' | 'auth' | 'health' | 'monitor' | 'tokens';

export interface ApiRouteContract {
  readonly id: string;
  readonly method: ApiMethod;
  readonly path: string;
  readonly auth: ApiAuthKind;
  readonly category: ApiRouteCategory;
}

export const apiRouteContracts = [
  route('healthz', 'GET', '/healthz', 'none', 'health'),
  route('adminLogin', 'POST', '/admin/login', 'none', 'auth'),
  route('agentTokenLogin', 'POST', '/auth/token/login', 'none', 'auth'),

  route('createReplayDataset', 'POST', '/admin/replay-datasets', 'adminSession', 'admin'),
  route('listReplayDatasets', 'GET', '/admin/replay-datasets', 'adminSession', 'admin'),
  route('getReplayDataset', 'GET', '/admin/replay-datasets/:id', 'adminSession', 'admin'),
  route('importReplayDataset', 'POST', '/admin/replay-datasets/:id/import', 'adminSession', 'admin'),
  route('createLiveAccount', 'POST', '/admin/live-accounts', 'adminSession', 'admin'),
  route('listLiveAccounts', 'GET', '/admin/live-accounts', 'adminSession', 'admin'),
  route('getLiveAccount', 'GET', '/admin/live-accounts/:id', 'adminSession', 'admin'),
  route('updateLiveAccount', 'PATCH', '/admin/live-accounts/:id', 'adminSession', 'admin'),
  route('deleteLiveAccount', 'DELETE', '/admin/live-accounts/:id', 'adminSession', 'admin'),
  route('createSandbox', 'POST', '/admin/sandboxes', 'adminSession', 'admin'),
  route('listSandboxes', 'GET', '/admin/sandboxes', 'adminSession', 'admin'),
  route('getSandbox', 'GET', '/admin/sandboxes/:id', 'adminSession', 'admin'),
  route('updateSandbox', 'PATCH', '/admin/sandboxes/:id', 'adminSession', 'admin'),
  route('deleteSandbox', 'DELETE', '/admin/sandboxes/:id', 'adminSession', 'admin'),
  route('startSandbox', 'POST', '/admin/sandboxes/:id/start', 'adminSession', 'admin'),
  route('pauseSandbox', 'POST', '/admin/sandboxes/:id/pause', 'adminSession', 'admin'),
  route('stopSandbox', 'POST', '/admin/sandboxes/:id/stop', 'adminSession', 'admin'),
  route('seekSandboxReplay', 'POST', '/admin/sandboxes/:id/replay/seek', 'adminSession', 'admin'),
  route('setSandboxReplaySpeed', 'POST', '/admin/sandboxes/:id/replay/speed', 'adminSession', 'admin'),
  route('resumeSandboxReplay', 'POST', '/admin/sandboxes/:id/replay/resume', 'adminSession', 'admin'),
  route('createSandboxAccount', 'POST', '/admin/sandboxes/:id/accounts', 'adminSession', 'admin'),
  route('listSandboxAccounts', 'GET', '/admin/sandboxes/:id/accounts', 'adminSession', 'admin'),
  route('getAdminAccount', 'GET', '/admin/accounts/:id', 'adminSession', 'admin'),
  route('updateAdminAccount', 'PATCH', '/admin/accounts/:id', 'adminSession', 'admin'),
  route('deleteAdminAccount', 'DELETE', '/admin/accounts/:id', 'adminSession', 'admin'),
  route('sandboxMonitorSnapshot', 'GET', '/admin/monitor/sandboxes/:id/snapshot', 'adminSession', 'monitor'),
  route('liveSymbolsSnapshot', 'GET', '/admin/monitor/live-symbols', 'adminSession', 'monitor'),

  route('createToken', 'POST', '/tokens', 'adminSession', 'tokens'),
  route('rotateToken', 'POST', '/tokens/:id/rotate', 'adminSession', 'tokens'),
  route('revokeToken', 'DELETE', '/tokens/:id', 'adminSession', 'tokens'),
  route('adminMonitorWebSocket', 'GET', '/ws/admin/monitor', 'adminSession', 'monitor'),

  route('marketPrice', 'GET', '/market/price', 'agentBearer', 'agent'),
  route('marketTicker', 'GET', '/market/ticker', 'agentBearer', 'agent'),
  route('marketKlines', 'GET', '/market/klines', 'agentBearer', 'agent'),
  route('sandboxTime', 'GET', '/sandbox/time', 'agentBearer', 'agent'),
  route('createOrder', 'POST', '/orders', 'agentBearer', 'agent'),
  route('listOrders', 'GET', '/orders', 'agentBearer', 'agent'),
  route('getOrder', 'GET', '/orders/:id', 'agentBearer', 'agent'),
  route('cancelOrder', 'POST', '/orders/:id/cancel', 'agentBearer', 'agent'),
  route('listTrades', 'GET', '/trades', 'agentBearer', 'agent'),
  route('listPositions', 'GET', '/positions', 'agentBearer', 'agent'),
  route('getAccount', 'GET', '/account', 'agentBearer', 'agent'),
  route('getAccountPerformance', 'GET', '/account/performance', 'agentBearer', 'agent'),
  route('accountWebSocket', 'GET', '/ws/account', 'agentBearer', 'agent'),
] as const satisfies readonly ApiRouteContract[];

export type ApiRoutePath = (typeof apiRouteContracts)[number]['path'];

export function findApiRouteContract(method: ApiMethod, path: string): ApiRouteContract | undefined {
  return apiRouteContracts.find((routeContract) => routeContract.method === method && routeContract.path === path);
}

function route(
  id: string,
  method: ApiMethod,
  path: string,
  auth: ApiAuthKind,
  category: ApiRouteCategory,
): ApiRouteContract {
  return { id, method, path, auth, category };
}
