package mcpserver

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// fakeDispatcher returns a fixed FD-shaped value.
type fakeDispatcher struct {
	lastName string
	lastArgs map[string]any
	value    any
}

func (f *fakeDispatcher) Call(_ *http.Request, name string, args map[string]any) (any, error) {
	f.lastName, f.lastArgs = name, args
	return f.value, nil
}

func post(t *testing.T, s *Server, body string, accept string) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(http.MethodPost, "/mcp", strings.NewReader(body))
	if accept != "" {
		req.Header.Set("Accept", accept)
	}
	rec := httptest.NewRecorder()
	s.ServeHTTP(rec, req)
	return rec
}

// decode reads a JSON-RPC response from either transport encoding.
func decode(t *testing.T, rec *httptest.ResponseRecorder) map[string]any {
	t.Helper()
	body := rec.Body.String()
	if idx := strings.Index(body, "data: "); idx >= 0 {
		body = strings.TrimSpace(body[idx+len("data: "):])
	}
	var out map[string]any
	if err := json.Unmarshal([]byte(body), &out); err != nil {
		t.Fatalf("decode %q: %v", rec.Body.String(), err)
	}
	return out
}

func newServer(t *testing.T, d Dispatcher) *Server {
	t.Helper()
	s, err := New(d)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	return s
}

// orderedNode is a JSON value decoded through json.Decoder's token stream
// instead of into a map[string]any, so object property order survives the
// round trip. json.Unmarshal into a Go map does not preserve key order,
// which would blind a structural-parity check to a property being
// reordered - exactly the kind of drift this test exists to catch. Every
// "description" key is dropped while decoding, since description text is
// intentionally excluded from the parity contract.
type orderedNode struct {
	kind  byte // 'o' object, 'a' array, 'v' scalar
	keys  []string
	elems []orderedNode
	value any
}

func decodeOrdered(dec *json.Decoder) (orderedNode, error) {
	tok, err := dec.Token()
	if err != nil {
		return orderedNode{}, err
	}
	delim, isDelim := tok.(json.Delim)
	if !isDelim {
		return orderedNode{kind: 'v', value: tok}, nil
	}
	switch delim {
	case json.Delim('{'):
		node := orderedNode{kind: 'o'}
		for dec.More() {
			keyTok, err := dec.Token()
			if err != nil {
				return orderedNode{}, err
			}
			key, ok := keyTok.(string)
			if !ok {
				return orderedNode{}, fmt.Errorf("object key was not a string: %v", keyTok)
			}
			child, err := decodeOrdered(dec)
			if err != nil {
				return orderedNode{}, err
			}
			if key == "description" {
				continue
			}
			node.keys = append(node.keys, key)
			node.elems = append(node.elems, child)
		}
		if _, err := dec.Token(); err != nil { // consume '}'
			return orderedNode{}, err
		}
		return node, nil
	case json.Delim('['):
		node := orderedNode{kind: 'a'}
		for dec.More() {
			child, err := decodeOrdered(dec)
			if err != nil {
				return orderedNode{}, err
			}
			node.elems = append(node.elems, child)
		}
		if _, err := dec.Token(); err != nil { // consume ']'
			return orderedNode{}, err
		}
		return node, nil
	default:
		return orderedNode{}, fmt.Errorf("unexpected delimiter: %v", delim)
	}
}

// canonical renders an orderedNode back to JSON text, preserving decoded
// object-key order and using json.Marshal only for the leaf tokens
// themselves (which have no ordering to lose).
func (n orderedNode) canonical() string {
	switch n.kind {
	case 'o':
		var b strings.Builder
		b.WriteByte('{')
		for i, key := range n.keys {
			if i > 0 {
				b.WriteByte(',')
			}
			keyJSON, _ := json.Marshal(key)
			b.Write(keyJSON)
			b.WriteByte(':')
			b.WriteString(n.elems[i].canonical())
		}
		b.WriteByte('}')
		return b.String()
	case 'a':
		var b strings.Builder
		b.WriteByte('[')
		for i, elem := range n.elems {
			if i > 0 {
				b.WriteByte(',')
			}
			b.WriteString(elem.canonical())
		}
		b.WriteByte(']')
		return b.String()
	default:
		valueJSON, _ := json.Marshal(n.value)
		return string(valueJSON)
	}
}

func canonicalStructure(t *testing.T, raw []byte) string {
	t.Helper()
	dec := json.NewDecoder(strings.NewReader(string(raw)))
	node, err := decodeOrdered(dec)
	if err != nil {
		t.Fatalf("decode structure: %v", err)
	}
	return node.canonical()
}

// field returns the child of an object-kind orderedNode stored under key,
// or the zero orderedNode if key is absent. Used to walk down to the
// "result.tools" slice of a decoded JSON-RPC response without ever passing
// through an unordered map[string]any, which would destroy the property
// order this test exists to check.
func (n orderedNode) field(key string) orderedNode {
	for i, k := range n.keys {
		if k == key {
			return n.elems[i]
		}
	}
	return orderedNode{}
}

// expectedToolSurfaceJSON pins the structural shape (tool names and order,
// parameter names and order, types, enums, defaults, and required arrays)
// this server must advertise. It is a description-stripped copy of
// tool_schemas.json taken when that file's structure was last reviewed;
// update it deliberately whenever the tool surface itself changes, never to
// silence a failing test. Description text is intentionally not part of
// this contract, since our own prose is free to evolve without touching
// the advertised interface.
const expectedToolSurfaceJSON = `[
  {
    "name": "get_beneficial_owners",
    "inputSchema": {
      "type": "object",
      "properties": {
        "name": {
          "type": "string"
        },
        "limit": {
          "type": "integer",
          "exclusiveMinimum": 0,
          "maximum": 1000,
          "default": 100
        }
      },
      "additionalProperties": false,
      "$schema": "http://json-schema.org/draft-07/schema#"
    }
  },
  {
    "name": "get_beneficial_ownership",
    "inputSchema": {
      "type": "object",
      "properties": {
        "ticker": {
          "type": "string"
        },
        "filer_cik": {
          "type": "string"
        },
        "type": {
          "type": "string",
          "enum": [
            "activist",
            "passive"
          ]
        },
        "history": {
          "type": "boolean"
        },
        "filing_date": {
          "type": "string",
          "pattern": "^\\d{4}-\\d{2}-\\d{2}$"
        },
        "filing_date_gte": {
          "type": "string",
          "pattern": "^\\d{4}-\\d{2}-\\d{2}$"
        },
        "filing_date_lte": {
          "type": "string",
          "pattern": "^\\d{4}-\\d{2}-\\d{2}$"
        },
        "limit": {
          "type": "integer",
          "exclusiveMinimum": 0,
          "maximum": 1000,
          "default": 10
        }
      },
      "additionalProperties": false,
      "$schema": "http://json-schema.org/draft-07/schema#"
    }
  },
  {
    "name": "get_company_facts",
    "inputSchema": {
      "type": "object",
      "properties": {
        "ticker": {
          "type": "string"
        },
        "cik": {
          "type": "string"
        }
      },
      "additionalProperties": false,
      "$schema": "http://json-schema.org/draft-07/schema#"
    }
  },
  {
    "name": "get_earnings",
    "inputSchema": {
      "type": "object",
      "properties": {
        "ticker": {
          "type": "string"
        }
      },
      "additionalProperties": false,
      "$schema": "http://json-schema.org/draft-07/schema#"
    }
  },
  {
    "name": "get_filing_items",
    "inputSchema": {
      "type": "object",
      "properties": {
        "ticker": {
          "type": "string"
        },
        "filing_type": {
          "type": "string",
          "enum": [
            "10-K",
            "10-Q",
            "8-K"
          ]
        },
        "year": {
          "type": "integer",
          "exclusiveMinimum": 0
        },
        "quarter": {
          "type": "integer",
          "exclusiveMinimum": 0,
          "minimum": 1,
          "maximum": 4
        },
        "accession_number": {
          "type": "string"
        },
        "item": {
          "type": "array",
          "items": {
            "type": "string",
            "enum": [
              "Item-1",
              "Item-1A",
              "Item-1B",
              "Item-2",
              "Item-3",
              "Item-4",
              "Item-5",
              "Item-6",
              "Item-7",
              "Item-7A",
              "Item-8",
              "Item-9",
              "Item-9A",
              "Item-9B",
              "Item-10",
              "Item-11",
              "Item-12",
              "Item-13",
              "Item-14",
              "Item-15",
              "Item-16",
              "Item-1.01",
              "Item-1.02",
              "Item-1.03",
              "Item-1.04",
              "Item-2.01",
              "Item-2.02",
              "Item-2.03",
              "Item-2.04",
              "Item-2.05",
              "Item-2.06",
              "Item-3.01",
              "Item-3.02",
              "Item-3.03",
              "Item-4.01",
              "Item-4.02",
              "Item-5.01",
              "Item-5.02",
              "Item-5.03",
              "Item-5.04",
              "Item-5.05",
              "Item-5.06",
              "Item-5.07",
              "Item-5.08",
              "Item-6.01",
              "Item-6.02",
              "Item-6.03",
              "Item-6.04",
              "Item-6.05",
              "Item-7.01",
              "Item-8.01",
              "Item-9.01"
            ]
          }
        }
      },
      "required": [
        "ticker",
        "filing_type"
      ],
      "additionalProperties": false,
      "$schema": "http://json-schema.org/draft-07/schema#"
    }
  },
  {
    "name": "list_filing_item_types",
    "inputSchema": {
      "type": "object",
      "properties": {},
      "additionalProperties": false,
      "$schema": "http://json-schema.org/draft-07/schema#"
    }
  },
  {
    "name": "get_filings",
    "inputSchema": {
      "type": "object",
      "properties": {
        "ticker": {
          "type": "string"
        },
        "cik": {
          "type": "string"
        },
        "limit": {
          "type": "integer",
          "exclusiveMinimum": 0,
          "maximum": 1000,
          "default": 100
        },
        "filing_type": {
          "type": "string"
        }
      },
      "additionalProperties": false,
      "$schema": "http://json-schema.org/draft-07/schema#"
    }
  },
  {
    "name": "get_financial_metrics",
    "inputSchema": {
      "type": "object",
      "properties": {
        "ticker": {
          "type": "string"
        },
        "period": {
          "type": "string",
          "enum": [
            "annual",
            "quarterly",
            "ttm"
          ],
          "default": "ttm"
        },
        "limit": {
          "type": "integer",
          "exclusiveMinimum": 0,
          "maximum": 100,
          "default": 4
        },
        "report_period_gte": {
          "type": "string",
          "pattern": "^\\d{4}-\\d{2}-\\d{2}$"
        },
        "report_period_lte": {
          "type": "string",
          "pattern": "^\\d{4}-\\d{2}-\\d{2}$"
        },
        "report_period_gt": {
          "type": "string",
          "pattern": "^\\d{4}-\\d{2}-\\d{2}$"
        },
        "report_period_lt": {
          "type": "string",
          "pattern": "^\\d{4}-\\d{2}-\\d{2}$"
        },
        "report_period": {
          "type": "string",
          "pattern": "^\\d{4}-\\d{2}-\\d{2}$"
        }
      },
      "required": [
        "ticker"
      ],
      "additionalProperties": false,
      "$schema": "http://json-schema.org/draft-07/schema#"
    }
  },
  {
    "name": "get_financial_metrics_snapshot",
    "inputSchema": {
      "type": "object",
      "properties": {
        "ticker": {
          "type": "string"
        }
      },
      "required": [
        "ticker"
      ],
      "additionalProperties": false,
      "$schema": "http://json-schema.org/draft-07/schema#"
    }
  },
  {
    "name": "get_balance_sheet",
    "inputSchema": {
      "type": "object",
      "properties": {
        "ticker": {
          "type": "string"
        },
        "period": {
          "type": "string",
          "enum": [
            "annual",
            "quarterly",
            "ttm"
          ],
          "default": "ttm"
        },
        "limit": {
          "type": "integer",
          "exclusiveMinimum": 0,
          "maximum": 100,
          "default": 4
        },
        "report_period_gte": {
          "type": "string",
          "pattern": "^\\d{4}-\\d{2}-\\d{2}$"
        },
        "report_period_lte": {
          "type": "string",
          "pattern": "^\\d{4}-\\d{2}-\\d{2}$"
        },
        "report_period_gt": {
          "type": "string",
          "pattern": "^\\d{4}-\\d{2}-\\d{2}$"
        },
        "report_period_lt": {
          "type": "string",
          "pattern": "^\\d{4}-\\d{2}-\\d{2}$"
        },
        "report_period": {
          "type": "string",
          "pattern": "^\\d{4}-\\d{2}-\\d{2}$"
        },
        "as_reported": {
          "type": "boolean",
          "default": false
        }
      },
      "required": [
        "ticker"
      ],
      "additionalProperties": false,
      "$schema": "http://json-schema.org/draft-07/schema#"
    }
  },
  {
    "name": "get_income_statement",
    "inputSchema": {
      "type": "object",
      "properties": {
        "ticker": {
          "type": "string"
        },
        "period": {
          "type": "string",
          "enum": [
            "annual",
            "quarterly",
            "ttm"
          ],
          "default": "ttm"
        },
        "limit": {
          "type": "integer",
          "exclusiveMinimum": 0,
          "maximum": 100,
          "default": 4
        },
        "report_period_gte": {
          "type": "string",
          "pattern": "^\\d{4}-\\d{2}-\\d{2}$"
        },
        "report_period_lte": {
          "type": "string",
          "pattern": "^\\d{4}-\\d{2}-\\d{2}$"
        },
        "report_period_gt": {
          "type": "string",
          "pattern": "^\\d{4}-\\d{2}-\\d{2}$"
        },
        "report_period_lt": {
          "type": "string",
          "pattern": "^\\d{4}-\\d{2}-\\d{2}$"
        },
        "report_period": {
          "type": "string",
          "pattern": "^\\d{4}-\\d{2}-\\d{2}$"
        },
        "as_reported": {
          "type": "boolean",
          "default": false
        }
      },
      "required": [
        "ticker"
      ],
      "additionalProperties": false,
      "$schema": "http://json-schema.org/draft-07/schema#"
    }
  },
  {
    "name": "get_cash_flow_statement",
    "inputSchema": {
      "type": "object",
      "properties": {
        "ticker": {
          "type": "string"
        },
        "period": {
          "type": "string",
          "enum": [
            "annual",
            "quarterly",
            "ttm"
          ],
          "default": "ttm"
        },
        "limit": {
          "type": "integer",
          "exclusiveMinimum": 0,
          "maximum": 100,
          "default": 4
        },
        "report_period_gte": {
          "type": "string",
          "pattern": "^\\d{4}-\\d{2}-\\d{2}$"
        },
        "report_period_lte": {
          "type": "string",
          "pattern": "^\\d{4}-\\d{2}-\\d{2}$"
        },
        "report_period_gt": {
          "type": "string",
          "pattern": "^\\d{4}-\\d{2}-\\d{2}$"
        },
        "report_period_lt": {
          "type": "string",
          "pattern": "^\\d{4}-\\d{2}-\\d{2}$"
        },
        "report_period": {
          "type": "string",
          "pattern": "^\\d{4}-\\d{2}-\\d{2}$"
        },
        "as_reported": {
          "type": "boolean",
          "default": false
        }
      },
      "required": [
        "ticker"
      ],
      "additionalProperties": false,
      "$schema": "http://json-schema.org/draft-07/schema#"
    }
  },
  {
    "name": "get_index_fund",
    "inputSchema": {
      "type": "object",
      "properties": {
        "ticker": {
          "type": "string"
        },
        "as_of": {
          "type": "string",
          "pattern": "^\\d{4}-\\d{2}-\\d{2}$"
        },
        "asset_class": {
          "type": "string",
          "enum": [
            "equity",
            "bond"
          ]
        },
        "limit": {
          "type": "integer",
          "exclusiveMinimum": 0,
          "maximum": 1000,
          "default": 50
        },
        "offset": {
          "type": "integer",
          "minimum": 0,
          "default": 0
        }
      },
      "required": [
        "ticker"
      ],
      "additionalProperties": false,
      "$schema": "http://json-schema.org/draft-07/schema#"
    }
  },
  {
    "name": "get_insider_ownership",
    "inputSchema": {
      "type": "object",
      "properties": {
        "ticker": {
          "type": "string"
        },
        "name": {
          "type": "string"
        },
        "form_type": {
          "type": "string",
          "enum": [
            "3",
            "3/A",
            "5",
            "5/A"
          ]
        },
        "filing_date": {
          "type": "string",
          "pattern": "^\\d{4}-\\d{2}-\\d{2}$"
        },
        "filing_date_gte": {
          "type": "string",
          "pattern": "^\\d{4}-\\d{2}-\\d{2}$"
        },
        "filing_date_lte": {
          "type": "string",
          "pattern": "^\\d{4}-\\d{2}-\\d{2}$"
        },
        "filing_date_gt": {
          "type": "string",
          "pattern": "^\\d{4}-\\d{2}-\\d{2}$"
        },
        "filing_date_lt": {
          "type": "string",
          "pattern": "^\\d{4}-\\d{2}-\\d{2}$"
        },
        "limit": {
          "type": "integer",
          "exclusiveMinimum": 0,
          "maximum": 1000,
          "default": 10
        }
      },
      "required": [
        "ticker"
      ],
      "additionalProperties": false,
      "$schema": "http://json-schema.org/draft-07/schema#"
    }
  },
  {
    "name": "get_insider_trades",
    "inputSchema": {
      "type": "object",
      "properties": {
        "ticker": {
          "type": "string"
        },
        "name": {
          "type": "string"
        },
        "filing_date": {
          "type": "string",
          "pattern": "^\\d{4}-\\d{2}-\\d{2}$"
        },
        "filing_date_gte": {
          "type": "string",
          "pattern": "^\\d{4}-\\d{2}-\\d{2}$"
        },
        "filing_date_lte": {
          "type": "string",
          "pattern": "^\\d{4}-\\d{2}-\\d{2}$"
        },
        "limit": {
          "type": "integer",
          "exclusiveMinimum": 0,
          "maximum": 5000,
          "default": 100
        }
      },
      "required": [
        "ticker"
      ],
      "additionalProperties": false,
      "$schema": "http://json-schema.org/draft-07/schema#"
    }
  },
  {
    "name": "get_institutional_investors",
    "inputSchema": {
      "type": "object",
      "properties": {
        "name": {
          "type": "string"
        }
      },
      "additionalProperties": false,
      "$schema": "http://json-schema.org/draft-07/schema#"
    }
  },
  {
    "name": "get_institutional_holdings",
    "inputSchema": {
      "type": "object",
      "properties": {
        "filer_cik": {
          "type": "string"
        },
        "ticker": {
          "type": "string"
        },
        "report_period": {
          "type": "string",
          "pattern": "^\\d{4}-\\d{2}-\\d{2}$"
        },
        "report_period_gte": {
          "type": "string",
          "pattern": "^\\d{4}-\\d{2}-\\d{2}$"
        },
        "report_period_lte": {
          "type": "string",
          "pattern": "^\\d{4}-\\d{2}-\\d{2}$"
        },
        "limit": {
          "type": "integer",
          "exclusiveMinimum": 0,
          "maximum": 200,
          "default": 10
        }
      },
      "additionalProperties": false,
      "$schema": "http://json-schema.org/draft-07/schema#"
    }
  },
  {
    "name": "get_interest_rates",
    "inputSchema": {
      "type": "object",
      "properties": {},
      "additionalProperties": false,
      "$schema": "http://json-schema.org/draft-07/schema#"
    }
  },
  {
    "name": "get_kpi_guidance",
    "inputSchema": {
      "type": "object",
      "properties": {
        "ticker": {
          "type": "string"
        },
        "period": {
          "type": "string",
          "enum": [
            "quarterly",
            "annual"
          ],
          "default": "quarterly"
        },
        "metric_name": {
          "type": "string"
        },
        "report_period_gte": {
          "type": "string",
          "pattern": "^\\d{4}-\\d{2}-\\d{2}$"
        },
        "report_period_lte": {
          "type": "string",
          "pattern": "^\\d{4}-\\d{2}-\\d{2}$"
        },
        "limit": {
          "type": "integer",
          "exclusiveMinimum": 0,
          "maximum": 50,
          "default": 4
        }
      },
      "required": [
        "ticker"
      ],
      "additionalProperties": false,
      "$schema": "http://json-schema.org/draft-07/schema#"
    }
  },
  {
    "name": "get_kpi_metrics",
    "inputSchema": {
      "type": "object",
      "properties": {
        "ticker": {
          "type": "string"
        },
        "period": {
          "type": "string",
          "enum": [
            "quarterly",
            "annual"
          ],
          "default": "quarterly"
        },
        "metric_name": {
          "type": "string"
        },
        "report_period_gte": {
          "type": "string",
          "pattern": "^\\d{4}-\\d{2}-\\d{2}$"
        },
        "report_period_lte": {
          "type": "string",
          "pattern": "^\\d{4}-\\d{2}-\\d{2}$"
        },
        "limit": {
          "type": "integer",
          "exclusiveMinimum": 0,
          "maximum": 50,
          "default": 4
        }
      },
      "required": [
        "ticker"
      ],
      "additionalProperties": false,
      "$schema": "http://json-schema.org/draft-07/schema#"
    }
  },
  {
    "name": "get_kpi_non_gaap",
    "inputSchema": {
      "type": "object",
      "properties": {
        "ticker": {
          "type": "string"
        },
        "period": {
          "type": "string",
          "enum": [
            "quarterly",
            "annual"
          ],
          "default": "quarterly"
        },
        "metric_name": {
          "type": "string"
        },
        "report_period_gte": {
          "type": "string",
          "pattern": "^\\d{4}-\\d{2}-\\d{2}$"
        },
        "report_period_lte": {
          "type": "string",
          "pattern": "^\\d{4}-\\d{2}-\\d{2}$"
        },
        "limit": {
          "type": "integer",
          "exclusiveMinimum": 0,
          "maximum": 50,
          "default": 4
        }
      },
      "required": [
        "ticker"
      ],
      "additionalProperties": false,
      "$schema": "http://json-schema.org/draft-07/schema#"
    }
  },
  {
    "name": "get_news",
    "inputSchema": {
      "type": "object",
      "properties": {
        "ticker": {
          "type": "string"
        },
        "limit": {
          "type": "integer",
          "exclusiveMinimum": 0,
          "maximum": 10,
          "default": 5
        }
      },
      "additionalProperties": false,
      "$schema": "http://json-schema.org/draft-07/schema#"
    }
  },
  {
    "name": "get_segmented_financials",
    "inputSchema": {
      "type": "object",
      "properties": {
        "ticker": {
          "type": "string"
        },
        "period": {
          "type": "string",
          "enum": [
            "annual",
            "quarterly"
          ],
          "default": "annual"
        },
        "limit": {
          "type": "integer",
          "exclusiveMinimum": 0,
          "maximum": 100,
          "default": 4
        },
        "report_period_gte": {
          "type": "string",
          "pattern": "^\\d{4}-\\d{2}-\\d{2}$"
        },
        "report_period_lte": {
          "type": "string",
          "pattern": "^\\d{4}-\\d{2}-\\d{2}$"
        },
        "report_period_gt": {
          "type": "string",
          "pattern": "^\\d{4}-\\d{2}-\\d{2}$"
        },
        "report_period_lt": {
          "type": "string",
          "pattern": "^\\d{4}-\\d{2}-\\d{2}$"
        },
        "report_period": {
          "type": "string",
          "pattern": "^\\d{4}-\\d{2}-\\d{2}$"
        }
      },
      "required": [
        "ticker"
      ],
      "additionalProperties": false,
      "$schema": "http://json-schema.org/draft-07/schema#"
    }
  },
  {
    "name": "get_stock_price",
    "inputSchema": {
      "type": "object",
      "properties": {
        "ticker": {
          "type": "string"
        }
      },
      "required": [
        "ticker"
      ],
      "additionalProperties": false,
      "$schema": "http://json-schema.org/draft-07/schema#"
    }
  },
  {
    "name": "get_stock_prices",
    "inputSchema": {
      "type": "object",
      "properties": {
        "ticker": {
          "type": "string"
        },
        "interval": {
          "type": "string",
          "enum": [
            "second",
            "minute",
            "day",
            "week",
            "month",
            "year"
          ],
          "default": "day"
        },
        "interval_multiplier": {
          "type": "integer",
          "exclusiveMinimum": 0,
          "default": 1
        },
        "start_date": {
          "type": "string",
          "pattern": "^\\d{4}-\\d{2}-\\d{2}$"
        },
        "end_date": {
          "type": "string",
          "pattern": "^\\d{4}-\\d{2}-\\d{2}$"
        }
      },
      "required": [
        "ticker"
      ],
      "additionalProperties": false,
      "$schema": "http://json-schema.org/draft-07/schema#"
    }
  },
  {
    "name": "screen_stocks",
    "inputSchema": {
      "type": "object",
      "properties": {
        "filters": {
          "type": "array",
          "items": {
            "type": "object",
            "properties": {
              "field": {
                "type": "string"
              },
              "operator": {
                "type": "string",
                "enum": [
                  "gt",
                  "lt",
                  "gte",
                  "lte",
                  "eq",
                  "in"
                ]
              },
              "value": {
                "anyOf": [
                  {
                    "type": "number"
                  },
                  {
                    "type": "string"
                  },
                  {
                    "type": "array",
                    "items": {
                      "type": [
                        "number",
                        "string"
                      ]
                    }
                  }
                ]
              }
            },
            "required": [
              "field",
              "operator",
              "value"
            ],
            "additionalProperties": false
          },
          "minItems": 1
        },
        "currency": {
          "type": "string",
          "default": "USD"
        },
        "limit": {
          "type": "integer",
          "exclusiveMinimum": 0,
          "maximum": 100,
          "default": 10
        }
      },
      "required": [
        "filters"
      ],
      "additionalProperties": false,
      "$schema": "http://json-schema.org/draft-07/schema#"
    }
  },
  {
    "name": "list_stock_screener_filters",
    "inputSchema": {
      "type": "object",
      "properties": {},
      "additionalProperties": false,
      "$schema": "http://json-schema.org/draft-07/schema#"
    }
  }
]`

// The advertised MCP surface must keep the exact tool names and order,
// parameter names and order, types, enums, defaults, and required arrays
// pinned in expectedToolSurfaceJSON. Description text is deliberately not
// part of this check: it is this project's own prose and may change freely
// as long as the functional interface behind it does not.
func TestToolsListMatchesStructuralSurface(t *testing.T) {
	s := newServer(t, &fakeDispatcher{})
	rec := post(t, s, `{"jsonrpc":"2.0","id":1,"method":"tools/list"}`, "")

	// Decoded straight from the raw response body through the token stream,
	// not through decode()'s json.Unmarshal into map[string]any: Go's map
	// decoding (and its later re-encoding) does not preserve JSON object key
	// order, which would blind this test to a property being reordered.
	body := rec.Body.String()
	if idx := strings.Index(body, "data: "); idx >= 0 {
		body = strings.TrimSpace(body[idx+len("data: "):])
	}
	root, err := decodeOrdered(json.NewDecoder(strings.NewReader(body)))
	if err != nil {
		t.Fatalf("decode response: %v", err)
	}
	advertised := root.field("result").field("tools")
	if advertised.kind != 'a' {
		t.Fatalf("result.tools was not a JSON array")
	}
	if len(advertised.elems) != 27 {
		t.Fatalf("advertised tool count = %d, want 27", len(advertised.elems))
	}

	gotStructure := advertised.canonical()
	wantStructure := canonicalStructure(t, []byte(expectedToolSurfaceJSON))

	if gotStructure != wantStructure {
		var gotNames, wantNames []string
		for _, tool := range advertised.elems {
			gotNames = append(gotNames, fmt.Sprintf("%v", tool.field("name").value))
		}
		want, err := decodeOrdered(json.NewDecoder(strings.NewReader(expectedToolSurfaceJSON)))
		if err != nil {
			t.Fatalf("decode expectedToolSurfaceJSON: %v", err)
		}
		for _, tool := range want.elems {
			wantNames = append(wantNames, fmt.Sprintf("%v", tool.field("name").value))
		}
		t.Fatalf("advertised tool structure drifted from expectedToolSurfaceJSON\n"+
			"advertised names: %v\nexpected names:   %v", gotNames, wantNames)
	}
}

func TestInitializeReportsServerInfo(t *testing.T) {
	s := newServer(t, &fakeDispatcher{})
	rec := post(t, s, `{"jsonrpc":"2.0","id":1,"method":"initialize","params":{}}`, "")
	result := decode(t, rec)["result"].(map[string]any)
	info := result["serverInfo"].(map[string]any)
	if info["name"] != serverName {
		t.Errorf("serverInfo.name = %v, want %s", info["name"], serverName)
	}
	if result["protocolVersion"] != protocolVersion {
		t.Errorf("protocolVersion = %v, want %s", result["protocolVersion"], protocolVersion)
	}
}

// List tools answer with the bare records array as text content, matching the
// upstream server (no wrapper object, no next_page_url).
func TestToolsCallReturnsBareArrayAsTextContent(t *testing.T) {
	records := []any{map[string]any{"ticker": "AAPL", "revenue": 416161000000}}
	d := &fakeDispatcher{value: records}
	s := newServer(t, d)

	rec := post(t, s, `{"jsonrpc":"2.0","id":2,"method":"tools/call","params":{"name":"get_income_statement","arguments":{"ticker":"AAPL"}}}`, "")
	result := decode(t, rec)["result"].(map[string]any)
	content := result["content"].([]any)
	text := content[0].(map[string]any)["text"].(string)

	var decoded any
	if err := json.Unmarshal([]byte(text), &decoded); err != nil {
		t.Fatalf("tool text was not JSON: %v", err)
	}
	if _, isArray := decoded.([]any); !isArray {
		t.Fatalf("tool result must be a bare array, got %T", decoded)
	}
	if d.lastName != "get_income_statement" || d.lastArgs["ticker"] != "AAPL" {
		t.Errorf("dispatch got %s %v", d.lastName, d.lastArgs)
	}
}

func TestUnknownToolIsRejected(t *testing.T) {
	s := newServer(t, &fakeDispatcher{})
	rec := post(t, s, `{"jsonrpc":"2.0","id":3,"method":"tools/call","params":{"name":"nope","arguments":{}}}`, "")
	if _, hasErr := decode(t, rec)["error"]; !hasErr {
		t.Fatal("unknown tool must produce a JSON-RPC error")
	}
}

func TestSSETransportWhenAccepted(t *testing.T) {
	s := newServer(t, &fakeDispatcher{})
	rec := post(t, s, `{"jsonrpc":"2.0","id":4,"method":"ping"}`, "application/json, text/event-stream")
	if ct := rec.Header().Get("Content-Type"); ct != "text/event-stream" {
		t.Fatalf("Content-Type = %q, want text/event-stream", ct)
	}
	if !strings.Contains(rec.Body.String(), "event: message") {
		t.Fatalf("SSE framing missing: %q", rec.Body.String())
	}
}

func TestNotificationsGetNoBody(t *testing.T) {
	s := newServer(t, &fakeDispatcher{})
	rec := post(t, s, `{"jsonrpc":"2.0","method":"notifications/initialized"}`, "")
	if rec.Code != http.StatusAccepted {
		t.Fatalf("status = %d, want 202", rec.Code)
	}
}
