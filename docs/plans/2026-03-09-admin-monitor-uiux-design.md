# Admin + Monitor UI/UX Design Doc

**Date:** 2026-03-09
**Product Scope:** Trading Sandbox Admin Console + Monitor Workspace
**Audience:** Frontend engineers, backend engineers, product/design collaborators
**Design Fidelity:** High-fidelity product specification for implementation, not a visual moodboard

---

## 1. Purpose

This document defines the P1 user experience for two operational roles built on top of the current P0 trading sandbox backend:
- **Admin** configures replay datasets, sandboxes, accounts, tokens, and live account metadata
- **Monitor Operator** watches replay/live system state, account performance, orders, trades, and risk signals in real time

The design assumes the existing P0 backend already supports sandbox/account/token/order/monitor primitives and that P1 will add replay dataset management, replay controls, live price cache visibility, and live account CRUD.

This document does **not** cover an agent self-service portal. All authenticated end-user surfaces are internal operations tools.

---

## 2. Product Principles

### 2.1 Operations-first, not marketing-first
- Dense information is acceptable if hierarchy is clear.
- The UI should feel like a control room, not a landing page.
- Status, freshness, and error visibility take priority over decoration.

### 2.2 One-way clarity for dangerous actions
- Token reveal, dataset import, replay seek, sandbox start/stop, and live account enable/disable must all surface irreversible or high-impact consequences before confirmation.
- Every state-changing control must have visible result feedback: success, partial success, failure, pending.

### 2.3 Real-time without silent drift
- If the WebSocket disconnects, the UI must show a degraded-state banner immediately.
- Freshness timestamps must appear on replay time, live symbol cache state, and monitor feeds.
- Polling fallback is secondary and visually marked as degraded.

### 2.4 Desktop-first execution surface
- Desktop is the primary operating environment.
- Tablet layouts remain usable but compressed.
- Mobile is read-only monitor mode; no destructive admin operations.

---

## 3. Personas and Jobs-to-be-Done

### 3.1 Admin
Primary goals:
- Import and validate replay data
- Bind datasets to sandboxes
- Create accounts and issue tokens
- Adjust replay time and speed safely
- Manage live account metadata and availability

Failure sensitivity:
- Cannot lose track of which dataset a sandbox is using
- Cannot accidentally reveal tokens twice without warning
- Cannot perform replay controls without immediate state reflection

### 3.2 Monitor Operator
Primary goals:
- Understand what sandboxes are active now
- Identify which accounts or symbols are moving abnormally
- Track trades, stop-loss triggers, and connection health
- Distinguish replay problems from live market cache problems

Failure sensitivity:
- Must see stale/disconnected state quickly
- Must be able to isolate one sandbox or one account without losing global context

---

## 4. Information Architecture

The product is split into two top-level workspaces.

### 4.1 Admin Console
Navigation style:
- Left navigation rail
- Sticky top status bar
- Workspace-level breadcrumb and action slot

Primary sections:
1. `Sandboxes`
2. `Replay Datasets`
3. `Accounts & Tokens`
4. `Live Accounts`
5. `System Health`

### 4.2 Monitor Workspace
Navigation style:
- Full-width dashboard layout optimized for widescreen displays
- Filter bar pinned below top status bar
- Panels arranged in a responsive grid with priority ordering

Primary sections:
1. `Global Overview`
2. `Sandbox Monitor Room`
3. `Performance Board`
4. `Orders & Trades Stream`
5. `Risk & Alerts`

---

## 5. Navigation Model

### 5.1 Global Top Bar
Persistent elements:
- Product title: `Trading Sandbox Control`
- Environment badge: `Replay`, `Mixed`, or `Live-Ready`
- WebSocket connection indicator
- Current user badge
- Global command/search trigger

Top bar states:
- `Connected`: green dot + last message freshness under 10s
- `Lagging`: amber dot + “data delayed” text
- `Disconnected`: red banner under top bar with reconnect CTA

### 5.2 Left Navigation for Admin Console
Order:
- Sandboxes
- Replay Datasets
- Accounts & Tokens
- Live Accounts
- System Health

Each item includes optional badges:
- Number of running sandboxes
- Dataset imports in progress
- Disabled accounts count
- Live symbol cache warnings

---

## 6. Page Specifications

### 6.1 Login
Purpose:
- Entry point for admin session login

Layout:
- Centered compact panel on neutral dark operations background
- Left side optional product statement and recent system status
- Right side login form

Fields:
- Username
- Password
- Sign-in button

States:
- Invalid credentials inline error
- Backend unavailable error banner
- Existing session redirect

Notes:
- Token login is not exposed in Admin Console UI; token auth remains agent-only in P1

### 6.2 Sandbox List
Purpose:
- Show all sandboxes and their current operational state

Key components:
- Filter bar: status, dataset, mode, search
- Table or card-list hybrid with density toggle
- Quick actions: create, start, pause, stop

Columns / card fields:
- Sandbox name / ID
- Status badge
- Bound dataset
- Current replay time
- Replay speed
- Account count
- Last updated

Primary CTA:
- `Create Sandbox`

Row actions:
- Open detail
- Start / pause / stop
- Delete if empty and safe

### 6.3 Sandbox Detail
Purpose:
- Single control plane for one sandbox

Layout:
- Upper control region
- Lower operational grid

Upper control region modules:
- Metadata card: name, ID, status, dataset, mode
- Replay control card: current time, seek input, speed selector, pause/resume/start buttons
- Dataset binding card: current dataset, change dataset CTA, validation summary

Lower grid modules:
- Accounts panel
- Tokens panel shortcut
- Open orders panel
- Trades panel
- Positions panel
- Event feed panel

Critical behavior:
- Replay seek or speed change should update the replay timeline immediately and visually pulse the updated time label
- Any pending limit/stop re-evaluation caused by replay control should surface as event feed items

### 6.4 Replay Dataset Library
Purpose:
- Manage datasets without DB access

Layout:
- Toolbar: import, search, symbol filter, interval filter
- Main grid: dataset cards or dense table
- Right-side detail drawer on selection

Dataset list fields:
- Name
- Symbol
- Interval
- Time range start/end
- Source
- Import status
- Row count
- Last import time

Actions:
- Import new dataset
- View validation result
- Bind to sandbox
- Archive or delete unused dataset

### 6.5 Dataset Import / Validation
Purpose:
- Guide admin through a safe import flow

Interaction model:
- Step wizard in a modal or dedicated page

Wizard steps:
1. Upload CSV
2. Schema check
3. Preview first rows + detected symbol/interval/range
4. Confirm import
5. Result summary

Validation messages must include:
- Missing required columns
- Non-monotonic timestamps
- Unsupported symbols or intervals
- Duplicate or malformed rows

Completion states:
- Completed with imported row count
- Failed with error summary and downloadable error report

### 6.6 Accounts & Tokens
Purpose:
- Manage virtual accounts inside sandboxes and the tokens bound to them

Layout:
- Sandbox filter + account table
- Detail drawer or side panel for selected account

Account detail modules:
- Account metadata
- Balance/equity summary
- Status toggle
- Token list
- Create token action
- Rotate token action

Token reveal pattern:
- Plaintext token appears once inside a high-emphasis reveal panel
- Warning copy: “This token will not be shown again”
- Separate `Copy` button and `Acknowledge` button
- After dismiss, only token metadata remains visible

### 6.7 Live Account Manager
Purpose:
- Manage live account metadata and readiness, not exchange execution

Layout:
- Dense list with environment/provider/status filters
- Detail page or side panel for one account

Fields:
- Provider
- Environment
- Credentials status
- Supported symbols
- Account status
- Last health check

Actions:
- Create live account
- Edit metadata
- Enable/disable
- Issue token
- View linked live symbol health

### 6.8 System Health / Live Symbol Cache
Purpose:
- Expose internal P1 live market cache state in operator-friendly form

Modules:
- Cache summary cards: active symbols, queued symbols, stale symbols, GC removals
- Live symbol table
- Connection log / updater health
- Recent cache events feed

Live symbol table columns:
- Symbol
- State
- Latest price
- Last updated
- Last accessed
- Subscriber count
- Source/provider

Status colors:
- `active` green
- `queued` blue
- `stale` amber
- `removing` muted red

---

## 7. Monitor Workspace Specifications

### 7.1 Global Overview
Purpose:
- 10-second understanding of total system state

Panels:
- Running sandboxes
- Active accounts
- Live symbols in cache
- Recent stop-loss events
- Connection health
- Replay vs live distribution

### 7.2 Sandbox Monitor Room
Purpose:
- Deep operational view of one sandbox

Filters:
- Sandbox selector
- Account selector
- Symbol selector

Panels:
- Replay timeline strip
- Account ranking by equity / return
- Open positions
- Open orders
- Recent trades
- Event feed

### 7.3 Account Performance Board
Purpose:
- Compare accounts across a sandbox or globally

Visuals:
- Rank table
- Equity sparkline or line chart
- PnL delta chips

Sorting defaults:
- Equity descending
- Realized PnL descending

### 7.4 Orders & Trades Stream
Purpose:
- High-cadence operational feed

Layout:
- Split columns for open orders and recent trades
- Event type chips and account/symbol filters

Feed item fields:
- Timestamp
- Sandbox or live source
- Account
- Symbol
- Order side/type
- Status transition

### 7.5 Risk / Alert Feed
Purpose:
- Surface anomalies and degraded operation states

Alert types:
- Stop-loss triggered
- Market data unavailable
- Live symbol stale
- WebSocket disconnected
- Dataset import failed
- Sandbox state mismatch

Behavior:
- Alerts persist until acknowledged or resolved
- Severity colors and icons differ clearly

---

## 8. Key Interaction Flows

### 8.1 Dataset Import to Sandbox Start
1. Admin enters `Replay Datasets`
2. Uploads CSV in wizard
3. Reviews validation preview
4. Confirms import
5. Opens target sandbox
6. Binds dataset
7. Creates accounts
8. Issues tokens
9. Starts sandbox
10. Jumps to monitor room

Success criteria:
- Entire flow can be completed in under 5 minutes by an experienced admin
- No step requires raw DB or CLI access

### 8.2 Replay Adjustment During Monitoring
1. Admin opens `Sandbox Detail`
2. Uses scrubber or exact datetime input to seek replay time
3. UI shows optimistic pending state on timeline controls
4. Sandbox state and event feed update after server confirmation
5. Monitor view reflects new replay time and any triggered fills/stops

Failure handling:
- If replay change fails, revert optimistic state and show inline error near control

### 8.3 Live Account Provisioning
1. Admin opens `Live Accounts`
2. Creates account metadata
3. Sets provider/environment/supported symbols
4. Verifies credentials status
5. Issues restricted token if needed
6. Opens `System Health` to confirm live symbols can enter cache lifecycle

---

## 9. Component-Level Specification

### 9.1 Summary Cards
Use for:
- Running sandboxes
- Active live symbols
- Pending imports
- Connection health

Card anatomy:
- Label
- Large primary metric
- Secondary trend or freshness label
- Optional warning badge

### 9.2 Equity Chart
Use in:
- Account Performance Board
- Sandbox Detail account drawer

Requirements:
- Hover or focus reveals timestamp and value
- Supports replay time scrub synchronization
- Must render empty state when insufficient history exists

### 9.3 Replay Timeline Scrubber
Requirements:
- Exact datetime input plus draggable scrubber
- Play/pause/resume buttons adjacent
- Speed selector (`0.5x`, `1x`, `2x`, `5x`, `10x`)
- Shows current replay time and dataset range limits

### 9.4 Dataset Upload Wizard
Requirements:
- Fixed, visible step list
- Preview table with first 10 valid rows
- Validation issue summary before import confirm
- Final state with imported row count and error CTA if failed

### 9.5 Token Reveal / Copy Action
Requirements:
- Token text masked by default until explicit reveal
- Reveal state expires on close/navigation
- Copy action logs as audit-worthy UI action
- Danger copy warns that plaintext is one-time only

### 9.6 Event Feed Item
Fields:
- Timestamp
- Severity marker
- Topic label
- Human-readable sentence
- Link to sandbox/account context

Examples:
- `Stop-loss triggered for acct_123 on BTCUSDT at replay 2025-01-01 00:01:00 UTC`
- `Live symbol BTCUSDT moved to stale after 65s without refresh`

### 9.7 Connection Status Banner
Requirements:
- Appears globally when websocket is degraded
- States: `connected`, `lagging`, `disconnected`, `fallback polling`
- Includes last successful message timestamp

---

## 10. State Design

Every data-heavy page must define these states:
- Loading
- Empty
- Error
- Partial data
- Disconnected realtime

Rules:
- Empty state should explain what to do next, not just say “No data”
- Error state should identify the failing subsystem when known: auth, dataset import, market data, websocket, sandbox control
- Disconnected realtime state must preserve last-known data but visibly mark it stale

Examples:
- `Replay Dataset Library` empty state: “No datasets imported yet. Upload a CSV OHLCV file to create the first replay dataset.”
- `System Health` disconnected state: “Live symbol status is stale. Reconnecting to update stream.”

---

## 11. Visual Direction

The interface should feel like a serious operations product.

Defaults:
- Background: layered graphite / slate surfaces, not pure black
- Accent system:
  blue for primary controls
  green for healthy/active
  amber for stale/warning
  red for destructive/critical
- Typography: compact, strong hierarchy, tabular numerals for finance metrics
- Charts: restrained, high-contrast, minimal ornament

Avoid:
- marketing gradients as dominant surface
- oversized hero sections
- excessive card padding that wastes monitor space
- ambiguous neutrals for important status indicators

---

## 12. Responsive Behavior

### Desktop
- Full feature set
- Multi-panel layouts
- Side drawers and dense tables

### Tablet
- Two-column monitor layouts collapse to stacked sections
- Sandbox detail remains usable, but token and dataset flows become full-screen panels

### Mobile
- Read-only monitor mode only
- Show overview cards, recent trades, alerts, connection status
- Hide destructive admin controls or move them behind a disabled-with-message pattern

---

## 13. Backend Contract Dependencies

UI implementation depends on these P1 contracts existing:
- Replay datasets list/detail/import endpoints
- Replay seek/speed/resume endpoints
- Live accounts CRUD endpoints
- Live symbol health endpoint
- Existing snapshot and websocket endpoints extended with P1 topics and freshness fields

UI should reuse existing P0 endpoints where possible:
- `/admin/sandboxes`
- `/admin/sandboxes/:id/accounts`
- `/tokens`
- `/admin/monitor/sandboxes/:id/snapshot`
- `/ws/admin/monitor`

---

## 14. Usability Acceptance Criteria

The design is acceptable only if all of the following are true:
- An admin can import a dataset, bind it, create accounts, issue tokens, and start a sandbox in under 5 minutes.
- A monitor operator can identify replay status, connection health, latest trades, and stop-loss events from a single workspace without opening multiple pages.
- Plaintext tokens are never re-displayed after initial reveal flow completion.
- Realtime disconnection is explicit and visually distinct from empty data.
- Live symbol cache health is understandable without backend implementation knowledge.

---

## 15. Implementation Notes for Frontend Engineers

Recommended delivery order:
1. Admin navigation shell and login
2. Sandbox List and Sandbox Detail
3. Replay Dataset Library and import wizard
4. Accounts & Tokens management
5. Live Account Manager
6. Monitor Workspace
7. System Health / Live Symbol Cache page
8. Realtime polish and degraded-state handling

Keep shared primitives small and explicit:
- status badges
- freshness labels
- event feed item
- metric cards
- timeline controls
- dense tables with filter bars

Do not delay degraded-state UI until the end; it is core product behavior.
