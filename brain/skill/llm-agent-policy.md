# LLM Agent Trading Policy

This policy defines the HTTP capability boundary for an LLM agent that trades inside a replay sandbox. The agent token is account-scoped and must never act as an admin-plane credential.

## Allowed Actions

- `GET /sandbox/time`
- `GET /market/price`
- `GET /market/ticker`
- `GET /market/klines` with `to <= sandbox current_time`
- `GET /account`
- `GET /account/performance`
- `GET /orders`
- `GET /positions`
- `GET /trades`
- `POST /orders`
- `POST /orders/:id/cancel`

## Forbidden Actions

- any `/admin/*` endpoint
- replay cursor control, including `/admin/sandboxes/:id/replay/seek`
- replay speed control, including `/admin/sandboxes/:id/replay/speed`
- future-candle queries where a requested market-data timestamp is after `GET /sandbox/time` `current_time`
- market price mutation paths
- direct database mutation

## Operating Rule

Before requesting candle history, the agent must call `GET /sandbox/time` and use the returned `current_time` as the hard upper bound. If data is unavailable at the sandbox clock, the agent should stop and report that the fixture lacks visible data rather than seeking, changing replay speed, or querying future candles.
