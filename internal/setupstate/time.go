package setupstate

// time.go — the unix-second clock, kept in its own file so a test can freeze it
// (the same convention as internal/prefixowner/time.go).

import "time"

var nowUnix = func() int64 { return time.Now().UTC().Unix() }
