// Package sharepro reads SharePro (StanSoft) databases for the migration
// tool. Importing it registers the mapper.
package sharepro

import "ledgerline/internal/migrate"

func init() { migrate.Register(New()) }
