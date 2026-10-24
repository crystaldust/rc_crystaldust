// Package schemas embeds the JSON Schemas that define Message Hub's
// file-based contracts, so that binaries validate against exactly the
// schema checked into the repository.
package schemas

import _ "embed"

// ConfigSchemaURL is the $id of the configuration schema.
const ConfigSchemaURL = "https://messagehub.internal/schemas/config.schema.json"

//go:embed config.schema.json
var configSchema []byte

// ConfigSchema returns a copy of the embedded configuration JSON Schema
// (Draft 2020-12). A copy is returned so callers cannot alter the embedded bytes.
func ConfigSchema() []byte {
	out := make([]byte, len(configSchema))
	copy(out, configSchema)
	return out
}
