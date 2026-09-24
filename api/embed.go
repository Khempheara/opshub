// Package api embeds the OpenAPI 3.1 specification, the source of truth for the REST API
// and for the generated TypeScript client (web/orval.config.ts).
package api

import _ "embed"

//go:embed openapi.yaml
var OpenAPISpec []byte
