# Trading Sandbox Web Console

React/Vite/Tailwind operations console for the trading sandbox backend. Admin functions (datasets, sandboxes, accounts, tokens, live metadata, system health) call real Go backend endpoints. The Agent Boundary screen is static documentation only.

## Prerequisites

- Node.js 18+
- Go 1.22+ (for the backend)

## Run with Real Backend

**1. Start the backend** (from repo root):

```sh
cd server && go run .
```

The backend listens on `http://127.0.0.1:7794` by default. Verify it is running:

```sh
curl http://127.0.0.1:7794/healthz
# Expected: {"message":"ok"}
```

**2. Start the frontend** (from repo root):

```sh
cd web && npm install && npm run dev
```

The frontend connects to `http://127.0.0.1:7794` by default (set `VITE_API_BASE_URL` to override).

**3. Login**

Open `http://localhost:5173` (or the address printed by Vite). The app starts at the login screen. Enter your admin credentials from the backend seed/env configuration.

## Backend-Backed Screens

| Screen | Backed by |
|---|---|
| Replay Datasets | `GET/POST /admin/replay-datasets`, `POST /admin/replay-datasets/:id/import` |
| Sandboxes | `GET/POST/DELETE /admin/sandboxes`, lifecycle endpoints |
| Sandbox Accounts | `GET/POST /admin/sandboxes/:id/accounts`, token create/revoke |
| Live Metadata | `GET/POST/DELETE /admin/live-accounts` |
| System Health | `GET /admin/monitor/live-symbols` |
| Sandbox Monitor | `GET /admin/monitor/sandboxes/:id/snapshot` |

## Static Documentation Screens

- **Agent Boundary**: Backend contract reference for agent integrators. No live data.

## Verify the App

```sh
cd web
npm test        # unit tests (vitest)
npm run lint    # type check (tsc --noEmit)
npm run build   # production build
```

The build emits a chunk size warning for the single-bundle output; this is non-blocking (exit code 0).

## Notes

- Session cookie is used for all admin requests (`credentials: include`). Logout is frontend-local only (backend has no logout endpoint).
- No live trading, strategy generation, signal generation, or exchange key management is supported.
