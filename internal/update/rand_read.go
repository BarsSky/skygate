package update

// rand_read.go — small wrapper around crypto/rand.Read so
// the rest of the update package doesn't have to import it
// directly. Splitting the import into a one-line file keeps
// the import-list audit (grep "import" internal/update/*.go)
// narrow.

import (
	cryptorand "crypto/rand"
)

func init() {
	randReadOS = func(b []byte) (int, error) {
		return cryptorand.Read(b)
	}
}
