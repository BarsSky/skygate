#!/bin/bash
# Simulate the handler's "Provision" effect: read the API key
# from the per-user headscale, encrypt with SKYGATE_SECRET_KEY,
# write to portal_users. Run the Go program INSIDE the skygate
# container so it can read /data/skygate.db directly.
set -e

cd /home/skyadmin/skygate
set -a
source .env
set +a
export SKYGATE_SECRET_KEY

API_KEY=$(docker exec headscale-skyadmin /ko-app/headscale apikeys list 2>/dev/null \
    | grep -oE 'hskey-api-[A-Za-z0-9_-]+' | head -1)
if [ -z "$API_KEY" ]; then
    echo "FAIL: no API key on per-user headscale"
    exit 1
fi
echo "API key from per-user headscale: ${API_KEY:0:30}..."

# Stage the Go source inside the bind-mounted skygate dir.
mkdir -p cmd/_encrypt_tmp
cat > cmd/_encrypt_tmp/main.go <<'EOF'
package main

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"database/sql"
	"encoding/base64"
	"fmt"
	"os"

	_ "github.com/mattn/go-sqlite3"
)

func main() {
	hexKey := os.Getenv("SKYGATE_SECRET_KEY")
	if len(hexKey) != 64 {
		fmt.Fprintln(os.Stderr, "SKYGATE_SECRET_KEY must be 32 bytes (64 hex chars), got", len(hexKey))
		os.Exit(1)
	}
	key, err := hexDecode(hexKey)
	if err != nil {
		fmt.Fprintln(os.Stderr, "hex decode:", err)
		os.Exit(1)
	}
	plaintext := os.Args[1]
	url := os.Args[2]

	block, err := aes.NewCipher(key)
	if err != nil { fmt.Fprintln(os.Stderr, "aes:", err); os.Exit(1) }
	gcm, err := cipher.NewGCM(block)
	if err != nil { fmt.Fprintln(os.Stderr, "gcm:", err); os.Exit(1) }
	nonce := make([]byte, gcm.NonceSize())
	if _, err := rand.Read(nonce); err != nil { fmt.Fprintln(os.Stderr, "rand:", err); os.Exit(1) }
	ct := gcm.Seal(nil, nonce, []byte(plaintext), nil)
	blob := append(nonce, ct...)
	enc := base64.StdEncoding.EncodeToString(blob)

	db, err := sql.Open("sqlite3", "/data/skygate.db")
	if err != nil { fmt.Fprintln(os.Stderr, "db open:", err); os.Exit(1) }
	defer db.Close()
	_, err = db.Exec(`UPDATE portal_users SET headscale_url = ?, headscale_api_key_enc = ? WHERE username = 'skyadmin'`, url, enc)
	if err != nil { fmt.Fprintln(os.Stderr, "db write:", err); os.Exit(1) }
	fmt.Println("OK: wrote url + encrypted key to portal_users (skyadmin)")
}

func hexDecode(s string) ([]byte, error) {
	if len(s)%2 != 0 { return nil, fmt.Errorf("odd length") }
	out := make([]byte, len(s)/2)
	for i := 0; i < len(s); i += 2 {
		hi, ok := hexVal(s[i]); if !ok { return nil, fmt.Errorf("bad hex at %d", i) }
		lo, ok := hexVal(s[i+1]); if !ok { return nil, fmt.Errorf("bad hex at %d", i) }
		out[i/2] = byte(hi<<4 | lo)
	}
	return out, nil
}

func hexVal(c byte) (int, bool) {
	switch {
	case c >= '0' && c <= '9': return int(c - '0'), true
	case c >= 'a' && c <= 'f': return int(c-'a') + 10, true
	case c >= 'A' && c <= 'F': return int(c-'A') + 10, true
	}
	return 0, false
}
EOF

# Run the Go program INSIDE the skygate container (which has
# the DB on /data and Go installed for the build).
docker exec -e SKYGATE_SECRET_KEY="$SKYGATE_SECRET_KEY" \
    -w /app \
    skygate /usr/local/go/bin/go run ./cmd/_encrypt_tmp "$API_KEY" "http://headscale-skyadmin:50451"

# Clean up the temp dir from the host.
rm -rf cmd/_encrypt_tmp
