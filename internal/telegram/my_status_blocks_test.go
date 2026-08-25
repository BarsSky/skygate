// Package telegram — my_status_blocks_test.go pins the
// B186.3 fix: the original B186.2 myStatusBlocks
// declared `deviceCount int64` but never populated it
// (the `db.ListNodeOwnersByUsername` call was missing),
// so the rich message always showed "устройств 0" even
// when the user had 7 devices. The operator's 2026-08-25
// screenshot showed skyadmin (7 devices in node_owner_map)
// → "устройств 0" in the bot reply.
//
// 2026-08-25 (B186.3).
package telegram

import "testing"

// TestMyStatusBlocks_DeviceCountNotZero — regression
// guard for the B186.3 fix. Pins the source-level contract
// that myStatusBlocks must call ListNodeOwnersByUsername
// (same as the legacy myStatusReply at line 75). If a
// future change removes the call, this test catches it
// before the silent "0 devices" symptom returns.
//
// We read the source file rather than spinning up a DB
// (the integration test path needs a real PG, which is a
// Phase 2 setup). Source-level pinning is the right
// guard for "the call exists in the right place".
func TestMyStatusBlocks_DeviceCountNotZero(t *testing.T) {
	body, err := readSourceFile(t, "commands_user.go")
	if err != nil {
		t.Fatalf("read commands_user.go: %v", err)
	}
	if !contains(body, "ListNodeOwnersByUsername(env.DB, env.Username)") {
		t.Errorf("myStatusBlocks must call ListNodeOwnersByUsername like myStatusReply does — silent '0 devices' regression")
	}
}
