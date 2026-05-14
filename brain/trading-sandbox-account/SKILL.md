# Trading Sandbox Agent Smoke Test

Use this skill to verify that an existing trading sandbox account can be used end to end by an LLM trading agent.

## Scope

This test only covers the agent trading plane.

The agent token must:

- follow the sandbox clock;
- read only market data available up to the sandbox current time;
- trade only through account-scoped endpoints;
- never use replay controls, dataset setup, sandbox setup, account creation, or direct database changes.

## Allowed Agent Endpoints

- `GET /sandbox/time`
- `GET /market/price`
- `GET /market/ticker`
- `GET /market/klines?to=<sandbox_current_time>`
- `GET /account`
- `GET /account/performance`
- `GET /orders`
- `GET /positions`
- `GET /trades`
- `POST /orders`
- `POST /orders/:id/cancel`

## Forbidden Agent Actions

- Any `/admin/*` endpoint
- Replay seek, speed, cursor, or dataset controls
- Any future-candle request
- Any market price mutation path
- Any direct database write

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
```

## Health Check

```powershell
Invoke-RestMethod "$Base/healthz"
```

Expected:

```json
{"message":"ok"}
```

## Token Login Check

```powershell
curl.exe -s -H "Content-Type: application/json" `
  -d "{`"token`":`"$Token`"}" `
  "$Base/auth/token/login"
```

Expected:

- response includes `account_id`;
- response includes sandbox id;
- response includes the required scopes.

## Sandbox Clock Check

```powershell
$SandboxTimeJson = curl.exe -s -H "Authorization: Bearer $Token" `
  "$Base/sandbox/time"

$SandboxTime = $SandboxTimeJson | ConvertFrom-Json
$CurrentTime = [uri]::EscapeDataString($SandboxTime.current_time)

$SandboxTimeJson
```

Expected:

- `sandbox_id` exists;
- `current_time` exists;
- `status` is usable, normally `running`.

Use `current_time` as the hard upper bound for every market-data request.

## Market Data Check

```powershell
curl.exe -s -H "Authorization: Bearer $Token" `
  "$Base/market/ticker?symbol=BTCUSDT"

curl.exe -s -H "Authorization: Bearer $Token" `
  "$Base/market/klines?symbol=BTCUSDT&interval=1m&to=$CurrentTime"
```

Expected:

- ticker returns `BTCUSDT`;
- klines return data;
- no returned kline is later than sandbox `current_time`.

## Order Smoke Test

```powershell
$OrderJson = curl.exe -s -H "Authorization: Bearer $Token" `
  -H "Content-Type: application/json" `
  -d "{"symbol":"BTCUSDT","side":"buy","position_side":"long","order_type":"market","qty":1,"leverage":1}" `
  "$Base/orders"

$OrderJson
```

Expected:

- order is accepted;
- market order reaches `FILLED` when replay price is available.

## Account State Check

```powershell
curl.exe -s -H "Authorization: Bearer $Token" "$Base/account"
curl.exe -s -H "Authorization: Bearer $Token" "$Base/account/performance"
curl.exe -s -H "Authorization: Bearer $Token" "$Base/orders"
curl.exe -s -H "Authorization: Bearer $Token" "$Base/trades"
curl.exe -s -H "Authorization: Bearer $Token" "$Base/positions"
```

Expected:

- `/orders` includes the created order;
- `/trades` includes an execution;
- `/positions` includes a long `BTCUSDT` position;
- `/account` reflects updated margin, available balance, and equity.

## Pass Criteria

Report the sandbox account as usable only when all are true:

1. `/healthz` returns `ok`.
2. Token login succeeds.
3. `GET /sandbox/time` succeeds.
4. Market ticker succeeds.
5. Kline query uses `to <= sandbox current_time`.
6. No future-candle request is used.
7. Market order succeeds and reaches `FILLED`.
8. Trades and positions reflect the filled order.

## Failure Triage

- `401`: token missing, invalid, expired, or not passed as `Authorization: Bearer <token>`.
- `token scope is not sufficient`: recreate token with `market:read`, `trade:write`, `trade:read`, and `account:read`.
- `ACCOUNT_SANDBOX_REQUIRED`: token is not bound to a sandbox virtual account.
- `LIVE_ACCOUNT_TRADING_UNSUPPORTED`: use a virtual sandbox account.
- Missing ticker or klines: sandbox time may be outside dataset coverage.
- Order rejected or not filled: confirm sandbox is running, symbol matches dataset symbol, and replay price exists at current time.
