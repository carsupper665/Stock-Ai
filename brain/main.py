POLICY_MATCHING_SEMANTICS = {
    "exact": "method and path must match exactly",
    "path_template": "method and normalized route template must match",
    "path_prefix": "request path must start with path; method '*' matches all methods",
    "semantic": "caller must enforce the named pre-request policy guard",
}

ALLOWED_AGENT_ACTIONS = (
    {
        "method": "GET",
        "path": "/sandbox/time",
        "match": "exact",
        "constraint": "read sandbox current_time before candle history",
    },
    {"method": "GET", "path": "/market/price", "match": "exact", "constraint": "sandbox-scoped market read"},
    {"method": "GET", "path": "/market/ticker", "match": "exact", "constraint": "sandbox-scoped market read"},
    {
        "method": "GET",
        "path": "/market/klines",
        "match": "exact",
        "constraint": "to <= sandbox current_time from GET /sandbox/time",
    },
    {"method": "GET", "path": "/account", "match": "exact", "constraint": "account-scoped read"},
    {"method": "GET", "path": "/account/performance", "match": "exact", "constraint": "account-scoped read"},
    {"method": "GET", "path": "/orders", "match": "exact", "constraint": "account-scoped read"},
    {"method": "GET", "path": "/positions", "match": "exact", "constraint": "account-scoped read"},
    {"method": "GET", "path": "/trades", "match": "exact", "constraint": "account-scoped read"},
    {"method": "POST", "path": "/orders", "match": "exact", "constraint": "account-scoped trading write"},
    {
        "method": "POST",
        "path": "/orders/:id/cancel",
        "match": "path_template",
        "constraint": "account-scoped trading write",
    },
)

FORBIDDEN_AGENT_ACTIONS = (
    {"method": "*", "path": "/admin/", "match": "path_prefix", "reason": "admin-plane access is forbidden"},
    {
        "method": "POST",
        "path": "/admin/sandboxes/:id/replay/seek",
        "match": "path_template",
        "reason": "replay cursor control is admin-only",
    },
    {
        "method": "POST",
        "path": "/admin/sandboxes/:id/replay/speed",
        "match": "path_template",
        "reason": "replay speed control is admin-only",
    },
    {
        "method": "*",
        "path": "market data request after sandbox current_time",
        "match": "semantic",
        "reason": "future-candle market data query is forbidden",
    },
    {"method": "*", "path": "market price mutation", "match": "semantic", "reason": "agents are read/trade only"},
    {"method": "*", "path": "direct database mutation", "match": "semantic", "reason": "agents must use HTTP only"},
)

AGENT_MARKET_DATA_BOUNDARY = (
    "LLM agents must call GET /sandbox/time before candle requests and must not "
    "request future-candle data after the returned current_time."
)
