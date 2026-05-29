import { ApiClient } from './client';
import type { JsonValue } from './client';
import type { AdminAccount, Order, Position, Trade, AccountToken } from '../types';

type AdminTradingClient = Pick<ApiClient, 'request'>;

const defaultClient = new ApiClient();

// ── Accounts ─────────────────────────────────────────────────────────────────

interface AdminAccountResponse {
  readonly id: string;
  readonly name: string;
  readonly type: string;
  readonly status: string;
  readonly base_currency: string;
  readonly wallet_balance: number;
  readonly available_balance: number;
  readonly equity: number;
  readonly sandbox_id?: string;
  readonly environment?: string;
  readonly provider?: string;
  readonly price_mode?: string;
}

interface AdminAccountListResponse {
  readonly items: AdminAccountResponse[];
}

function mapAccount(r: AdminAccountResponse): AdminAccount {
  return {
    id: r.id,
    name: r.name,
    type: r.type === 'live' ? 'live' : 'virtual',
    status: r.status,
    base_currency: r.base_currency,
    wallet_balance: r.wallet_balance,
    available_balance: r.available_balance,
    equity: r.equity,
    sandbox_id: r.sandbox_id,
    environment: r.environment,
    provider: r.provider,
    price_mode: r.price_mode,
  };
}

export async function listAllAccounts(
  type?: 'live' | 'virtual',
  client: AdminTradingClient = defaultClient,
): Promise<AdminAccount[]> {
  const query = type ? `?type=${type}` : '';
  const response = await client.request<AdminAccountListResponse>(`/admin/accounts${query}`);
  return response.items.map(mapAccount);
}

export async function getAccountSummary(
  accountId: string,
  client: AdminTradingClient = defaultClient,
): Promise<AdminAccount> {
  const response = await client.request<AdminAccountResponse>(`/admin/accounts/${accountId}/summary`);
  return mapAccount(response);
}

// ── Orders ───────────────────────────────────────────────────────────────────

interface OrderResponse {
  readonly id: string;
  readonly account_id: string;
  readonly symbol: string;
  readonly side: 'buy' | 'sell';
  readonly position_side: 'long' | 'short';
  readonly order_type: 'market' | 'limit' | 'stop';
  readonly qty: number;
  readonly price?: number;
  readonly stop_price?: number;
  readonly leverage: number;
  readonly status: string;
  readonly filled_qty: number;
  readonly avg_fill_price?: number;
  readonly created_at: string;
  readonly updated_at: string;
}

interface OrderListResponse {
  readonly items: OrderResponse[];
}

function mapOrder(r: OrderResponse): Order {
  return {
    id: r.id,
    account_id: r.account_id,
    symbol: r.symbol,
    side: r.side,
    position_side: r.position_side,
    type: r.order_type,
    quantity: r.qty,
    price: r.price,
    stop_price: r.stop_price,
    leverage: r.leverage,
    status: r.status,
    filled_quantity: r.filled_qty,
    fill_price: r.avg_fill_price,
    created_at: r.created_at,
    updated_at: r.updated_at,
  };
}

export interface PlaceOrderInput {
  readonly symbol: string;
  readonly side: 'buy' | 'sell';
  readonly position_side: 'long' | 'short';
  readonly type: 'market' | 'limit' | 'stop';
  readonly quantity: number;
  readonly price?: number;
  readonly stop_price?: number;
  readonly leverage?: number;
}

export async function placeOrder(
  accountId: string,
  input: PlaceOrderInput,
  client: AdminTradingClient = defaultClient,
): Promise<Order> {
  const response = await client.request<OrderResponse>(`/admin/accounts/${accountId}/orders`, {
    method: 'POST',
    body: input as unknown as Record<string, JsonValue>,
  });
  return mapOrder(response);
}

export async function listOrders(
  accountId: string,
  client: AdminTradingClient = defaultClient,
): Promise<Order[]> {
  const response = await client.request<OrderListResponse>(`/admin/accounts/${accountId}/orders`);
  return response.items.map(mapOrder);
}

export async function getOrder(
  accountId: string,
  orderId: string,
  client: AdminTradingClient = defaultClient,
): Promise<Order> {
  const response = await client.request<OrderResponse>(`/admin/accounts/${accountId}/orders/${orderId}`);
  return mapOrder(response);
}

export async function cancelOrder(
  accountId: string,
  orderId: string,
  client: AdminTradingClient = defaultClient,
): Promise<Order> {
  const response = await client.request<OrderResponse>(`/admin/accounts/${accountId}/orders/${orderId}/cancel`, { method: 'POST' });
  return mapOrder(response);
}

// ── Trades & Positions ───────────────────────────────────────────────────────

interface TradeListResponse {
  readonly items: TradeResponse[];
}

interface PositionListResponse {
  readonly items: PositionResponse[];
}

interface PositionResponse {
  readonly id: string;
  readonly account_id: string;
  readonly symbol: string;
  readonly position_side: 'long' | 'short';
  readonly qty: number;
  readonly entry_price: number;
  readonly unrealized_pnl: number;
  readonly leverage: number;
  readonly updated_at: string;
}

interface TradeResponse {
  readonly id: string;
  readonly account_id: string;
  readonly order_id: string;
  readonly symbol: string;
  readonly side: 'buy' | 'sell';
  readonly position_side: 'long' | 'short';
  readonly qty: number;
  readonly price: number;
  readonly executed_at: string;
}

function mapPosition(r: PositionResponse): Position {
  return {
    id: r.id,
    account_id: r.account_id,
    symbol: r.symbol,
    position_side: r.position_side,
    quantity: r.qty,
    avg_entry_price: r.entry_price,
    unrealized_pnl: r.unrealized_pnl,
    realized_pnl: 0,
    leverage: r.leverage,
    updated_at: r.updated_at,
  };
}

function mapTrade(r: TradeResponse): Trade {
  return {
    id: r.id,
    account_id: r.account_id,
    order_id: r.order_id,
    symbol: r.symbol,
    side: r.side,
    position_side: r.position_side,
    quantity: r.qty,
    price: r.price,
    realized_pnl: 0,
    executed_at: r.executed_at,
  };
}

export async function listTrades(
  accountId: string,
  client: AdminTradingClient = defaultClient,
): Promise<Trade[]> {
  const response = await client.request<TradeListResponse>(`/admin/accounts/${accountId}/trades`);
  return response.items.map(mapTrade);
}

export async function listPositions(
  accountId: string,
  client: AdminTradingClient = defaultClient,
): Promise<Position[]> {
  const response = await client.request<PositionListResponse>(`/admin/accounts/${accountId}/positions`);
  return response.items.map(mapPosition);
}

// ── Tokens ───────────────────────────────────────────────────────────────────

interface TokenListResponse {
  readonly items: AccountToken[];
}

export interface CreateAdminTokenInput {
  readonly name: string;
  readonly scopes: readonly string[];
  readonly expiresAt?: string;
}

export interface CreatedToken extends AccountToken {
  readonly token: string;
}

export async function listTokens(
  accountId: string,
  client: AdminTradingClient = defaultClient,
): Promise<AccountToken[]> {
  const response = await client.request<TokenListResponse>(`/admin/accounts/${accountId}/tokens`);
  return response.items;
}

export async function createAdminToken(
  accountId: string,
  input: CreateAdminTokenInput,
  client: AdminTradingClient = defaultClient,
): Promise<CreatedToken> {
  const body: Record<string, JsonValue> = {
    name: input.name,
    scopes: [...input.scopes],
  };
  if (input.expiresAt !== undefined) {
    body.expires_at = input.expiresAt;
  }
  return client.request<CreatedToken>(`/admin/accounts/${accountId}/tokens`, {
    method: 'POST',
    body,
  });
}

export async function revokeAdminToken(
  accountId: string,
  tokenId: string,
  client: AdminTradingClient = defaultClient,
): Promise<void> {
  return client.request<void>(`/admin/accounts/${accountId}/tokens/${tokenId}`, { method: 'DELETE' });
}
