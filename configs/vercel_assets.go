// Package configassets встраивает публичные конфигурации только в deploy/vercel.
package configassets

import "embed"

// Files не содержит секретов; исходные JSON не копируются и не меняются.
//
//go:embed *.json
var Files embed.FS
