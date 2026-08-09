package migrations

import "embed"

// FS contains the ordered PostgreSQL migrations compiled into the API image.
//
//go:embed *.up.sql
var FS embed.FS
