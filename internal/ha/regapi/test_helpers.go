// test_helpers.go — placeholder. The full skygate test
// harness (sqlite3 + schema migrations + Patroni stub)
// is wired up in a follow-up B-check (B145.1). For
// B145 we cover only the pure-Go paths in
// credentials_test.go (Validate, storage keys,
// default timeout, sentinels). The DB round-trip
// tests for Save/Load land in B145.1 alongside the
// other integration test harness work.

package regapi
