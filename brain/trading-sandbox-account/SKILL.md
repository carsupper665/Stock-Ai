# Trading Sandbox Account Skill

Use this skill to smoke-check minimal sandbox account access for an LLM agent. It is only for account-scoped sandbox order and account actions, not for choosing trades or running a strategy.

Policy reference: `brain/skill/llm-agent-policy.md`.

## Scope

The agent token must:

- follow `GET /sandbox/time` before market reads;
- read only market data at or before sandbox `current_time`;
- read only the bound account, orders, positions, trades, and performance;
- place or cancel orders only through account-scoped order endpoints;
- stop and report missing fixture data instead of changing replay state or requesting hidden data.

## Allowed Agent Endpoints

Only these endpoints are in scope:

- `GET /sandbox/time`
- `GET /market/price`
- `GET /market/ticker`
- `GET /market/klines?to=<sandbox_current_time>`
- `GET /account`
- `GET /account/performance`
- `GET /orders`
- `GET /orders/:id`
- `GET /positions`
- `GET /trades`
- `POST /orders`
- `POST /orders/:id/cancel`

## Forbidden Scope

Do not use this skill for:

- strategy logic, trade selection, or portfolio planning;
- generate signal workflows or indicator-driven recommendations;
- autonomous loop execution, schedulers, daemons, or repeated self-directed trading;
- replay controls, including seek, cursor, speed, pause, resume, or dataset switching;
- admin APIs, dataset APIs, sandbox setup, account creation, token creation, or token rotation;
- direct DB mutation, fixture mutation, market mutation, or synthetic price injection;
- future candles or any market-data timestamp after sandbox `current_time`;
- live execution enablement, live-account trading, exchange-key setup, or testnet/mainnet routing;
- backtest expansion, optimization, walk-forward analysis, or research pipelines.

## Preconditions

Run commands from the repository root unless stated otherwise.

1. The backend is already running.
2. A replay dataset has already been imported.
3. A sandbox already exists and is running.
4. A virtual sandbox account already exists.
5. You already have an account bearer token with these scopes:
   - `market:read`
   - `trade:write`
   - `trade:read`
   - `account:read`

Default backend URL:

```powershell
$Base = "http://localhost:7794"
$Token = "<ACCOUNT_BEARER_TOKEN>"
$Headers = @{ Authorization = "Bearer $Token" }
```

## Token Check

```powershell
curl.exe -s -H "Content-Type: application/json" `
  -d "{`"token`":`"$Token`"}" `
  "$Base/auth/token/login"
```

Expected: response includes the account id, sandbox id, and the required scopes.

## Sandbox Time And Market Reads

```powershell
$SandboxTime = Invoke-RestMethod -Headers $Headers "$Base/sandbox/time"
$CurrentTime = [uri]::EscapeDataString($SandboxTime.current_time)

Invoke-RestMethod -Headers $Headers "$Base/market/price?symbol=BTCUSDT"
Invoke-RestMethod -Headers $Headers "$Base/market/ticker?symbol=BTCUSDT"
Invoke-RestMethod -Headers $Headers "$Base/market/klines?symbol=BTCUSDT&interval=1m&to=$CurrentTime"
```

Expected: market responses are for `BTCUSDT`, and every kline is at or before sandbox `current_time`.

## Account Reads

```powershell
Invoke-RestMethod -Headers $Headers "$Base/account"
Invoke-RestMethod -Headers $Headers "$Base/account/performance"
Invoke-RestMethod -Headers $Headers "$Base/orders"
Invoke-RestMethod -Headers $Headers "$Base/positions"
Invoke-RestMethod -Headers $Headers "$Base/trades"
```

Expected: each request returns only the account bound to the bearer token.

## Place, Read, And Cancel Order

Use this smoke payload exactly unless the existing fixture requires a different symbol.

```powershell
$OrderBody = @{
  symbol = "BTCUSDT"
  side = "buy"
  position_side = "long"
  order_type = "market"
  qty = 1
  leverage = 1
} | ConvertTo-Json -Compress

$Order = Invoke-RestMethod -Method Post -Headers $Headers `
  -ContentType "application/json" -Body $OrderBody "$Base/orders"

Invoke-RestMethod -Headers $Headers "$Base/orders/$($Order.id)"

Invoke-RestMethod -Method Post -Headers $Headers "$Base/orders/$($Order.id)/cancel"
```

Expected:

- `POST /orders` accepts the `BTCUSDT` `market` `buy` `long` payload with `qty: 1` and `leverage: 1`.
- `GET /orders/:id` returns the created account order.
- `POST /orders/:id/cancel` returns the cancelled order or the current order state when the market order already filled.

## Pass Criteria

Report the sandbox account as usable only when all are true:

1. Token login succeeds for the account token.
2. `GET /sandbox/time` returns `current_time` and usable status.
3. Market reads use `to <= sandbox current_time`.
4. Account, performance, orders, positions, and trades reads succeed.
5. `POST /orders` accepts the short smoke payload.
6. `GET /orders/:id` returns the account-owned order.
7. `POST /orders/:id/cancel` is available for cancellable orders.
8. No forbidden endpoint or forbidden scope action is used.

## Failure Triage

- `401`: token missing, invalid, expired, or not passed as `Authorization: Bearer <token>`.
- `token scope is not sufficient`: recreate token with `market:read`, `trade:write`, `trade:read`, and `account:read`.
- `ACCOUNT_SANDBOX_REQUIRED`: token is not bound to a sandbox virtual account.
- `LIVE_ACCOUNT_TRADING_UNSUPPORTED`: use a virtual sandbox account.
- Missing ticker or klines: sandbox time may be outside dataset coverage; do not use replay controls to compensate.
- Order rejected: confirm sandbox is running, symbol is `BTCUSDT`, and a replay price exists at sandbox `current_time`.
