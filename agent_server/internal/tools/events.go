package tools

func eventSchemas() []map[string]any {
	return []map[string]any{
		{
			"name": "create_event",
			"description": "Create a one-shot wake-up condition for this Session. type=timer needs params.at (RFC3339, future); " +
				"price_above/price_below/price_cross_above/price_cross_below need params.symbol and params.price (market defaults to crypto); " +
				"price_change_pct needs params.symbol, params.base_price, params.direction (up|down) and params.pct; " +
				"position_close needs params.position_id and fires only after that bound-Account position has first been observed open and is later absent. " +
				"A position closed before its first successful observation does not fire this event. " +
				"expires_at is optional RFC3339 (timer default: at + 1h; other events default: now + 24h). At most 10 active events per Session.",
			"input_schema": map[string]any{
				"type": "object", "properties": map[string]any{
					"type": map[string]any{"type": "string", "enum": []string{
						"timer", "price_above", "price_below", "price_cross_above", "price_cross_below", "price_change_pct", "position_close",
					}},
					"params":     map[string]any{"type": "object"},
					"expires_at": map[string]any{"type": "string", "format": "date-time"},
				}, "required": []string{"type", "params"}, "additionalProperties": false,
			},
		},
		{
			"name": "delete_event", "description": "Delete one of this Session's active events by id.",
			"input_schema": map[string]any{
				"type": "object", "properties": map[string]any{"event_id": map[string]any{"type": "string"}},
				"required": []string{"event_id"}, "additionalProperties": false,
			},
		},
		{
			"name": "list_events", "description": "List this Session's active events.",
			"input_schema": map[string]any{"type": "object", "properties": map[string]any{}, "additionalProperties": false},
		},
	}
}
