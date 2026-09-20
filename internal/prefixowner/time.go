package prefixowner

import "time"

// nowUnix is a seam for tests (they only assert that updated_at is
// non-zero, but keeping it in one place documents the intent).
func nowUnix() int64 { return time.Now().Unix() }
