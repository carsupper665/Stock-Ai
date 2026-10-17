package tools

func memoryDefinitions() []map[string]any {
	id := map[string]any{"type": "integer", "minimum": 1}
	text := func(maxLength int) map[string]any {
		return map[string]any{"type": "string", "minLength": 1, "maxLength": maxLength}
	}
	return []map[string]any{
		{"name": "memory_list", "description": "List labels (id, title, created_at) in this Session's private memory cabinet, newest first, at most 20. " +
			"Narrow with a title keyword (query), within_hours, from/to (YYYY-MM-DD or RFC3339, UTC) or before_id/after_id to page. " +
			"Read the titles first, then memory_get only the memories that matter.", "input_schema": map[string]any{
			"type": "object", "properties": map[string]any{
				"query": map[string]any{"type": "string"}, "within_hours": map[string]any{"type": "number", "exclusiveMinimum": 0},
				"from": map[string]any{"type": "string"}, "to": map[string]any{"type": "string"},
				"before_id": id, "after_id": id, "limit": map[string]any{"type": "integer", "minimum": 1, "maximum": 20, "default": 20},
			}, "additionalProperties": false,
		}},
		{"name": "memory_get", "description": "Open one memory from the cabinet by id and read its full msg.", "input_schema": map[string]any{
			"type": "object", "properties": map[string]any{"id": id}, "required": []string{"id"}, "additionalProperties": false,
		}},
		{"name": "memory_save", "description": "Save a memory for later Runs as soon as you learn it: a lesson, a strategy rule, an account constraint, " +
			"your plan for the next Run, or a user instruction. Not for trades, orders, positions or prices (they are in the ledger and tools). " +
			"The cabinet holds 60 memories. bring_back=true pins it into every Run's starting context; all pinned msgs together may hold 3000 characters.",
			"input_schema": map[string]any{
				"type": "object", "properties": map[string]any{"title": text(100), "msg": text(800), "bring_back": map[string]any{"type": "boolean", "default": false}},
				"required": []string{"title", "msg"}, "additionalProperties": false,
			}},
		{"name": "memory_edit", "description": "Change a memory's title, msg or bring_back by id. Prefer this over saving a duplicate; " +
			"when the cabinet is full, edit the least useful memory.", "input_schema": map[string]any{
			"type": "object", "properties": map[string]any{"id": id, "title": text(100), "msg": text(800), "bring_back": map[string]any{"type": "boolean"}},
			"required": []string{"id"}, "additionalProperties": false,
		}},
		{"name": "get_run_history", "description": "Read one completed, failed, or interrupted Run from this Session's History.", "input_schema": map[string]any{
			"type": "object", "properties": map[string]any{"run_id": map[string]any{"type": "integer", "minimum": 1}},
			"required": []string{"run_id"}, "additionalProperties": false,
		}},
	}
}
