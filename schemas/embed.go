// Package schemas embeds the canonical WorkOS schemas at their single source
// of truth. The JSON Schema files in this directory are the only manifest
// rule definitions; application code must load them from here instead of
// duplicating the rules in Go structs, Proto, or TypeScript.
package schemas

import _ "embed"

// AppManifestV1 holds the canonical App manifest v1 JSON Schema bytes. The
// schema's `$id` is read from these bytes and used as its resource URL.
//
//go:embed workos-app-manifest-v1.schema.json
var AppManifestV1 []byte
