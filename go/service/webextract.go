// This file ports web_extract.py: the Context.dev /web/extract request
// body and response-envelope handling shared by get_segmented_financials
// and the three get_kpi_* tools.
package service

import (
	"encoding/json"

	"github.com/belazy/monid-finance/providers"
)

// extractEndpoint mirrors web_extract.EXTRACT_ENDPOINT.
const extractEndpoint = "/web/extract"

// extractRequestBody mirrors web_extract.extract_request: one page,
// fact-checked, with the caller-supplied JSON Schema and instructions.
func extractRequestBody(url string, schema map[string]any, instructions string) map[string]any {
	return map[string]any{
		"url":          url,
		"schema":       schema,
		"instructions": instructions,
		"factCheck":    true,
		"maxPages":     1,
		"maxDepth":     0,
	}
}

// parseExtractOutput mirrors web_extract.parse_extract_output: validates
// the {status, url, urls_analyzed, data} envelope and returns the
// extracted data object.
func parseExtractOutput(raw json.RawMessage) (map[string]any, error) {
	var value any
	if err := json.Unmarshal(raw, &value); err != nil {
		return nil, &providers.SchemaDriftError{Msg: "Context.dev extract payload must be an object"}
	}
	payload, ok := value.(map[string]any)
	if !ok {
		return nil, &providers.SchemaDriftError{Msg: "Context.dev extract payload must be an object"}
	}
	data, hasData := payload["data"].(map[string]any)
	_, hasStatus := payload["status"]
	if !hasData || !hasStatus {
		return nil, &providers.SchemaDriftError{Msg: "Context.dev extract envelope is missing status or data"}
	}
	if status, _ := payload["status"].(string); status != "ok" {
		return nil, &providers.SchemaDriftError{Msg: "Context.dev extract did not report status ok"}
	}
	return data, nil
}
