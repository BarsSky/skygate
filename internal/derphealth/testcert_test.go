package derphealth

// Self-signed cert + key for TestProbeOne_HandshakeOK.
// Generated via crypto/ecdsa at module bring-up; valid
// until 2031, so tests don't expire on rebuild. Includes
// 127.0.0.1 + ::1 + localhost in SANs so the test
// httptest.NewUnstartedServer can verify against the
// Loopback IP.
const testCertPEM = `-----BEGIN CERTIFICATE-----
MIIBbDCCARKgAwIBAgIBATAKBggqhkjOPQQDAjAUMRIwEAYDVQQDEwlsb2NhbGhv
c3QwHhcNMjYwODI2MTgxNTU1WhcNMzEwODI2MTkxNTU1WjAUMRIwEAYDVQQDEwls
b2NhbGhvc3QwWTATBgcqhkjOPQIBBggqhkjOPQMBBwNCAAS+JHiA8zI5j/V72/xA
sgzbheWjBaPvhJORz2nYdfLrnZb+TvRH3BWGhqYXeNc7bl9zSGO1qoUPiss8jUel
KDMro1UwUzAOBgNVHQ8BAf8EBAMCBaAwEwYDVR0lBAwwCgYIKwYBBQUHAwEwLAYD
VR0RBCUwI4IJbG9jYWxob3N0hwR/AAABhxAAAAAAAAAAAAAAAAAAAAABMAoGCCqG
SM49BAMCA0gAMEUCIQDofxdqRB9W6iqNFz952VatFigwra5QSBcpPBFD5Ekx8QIg
Rn5aOvWl7Llq9XVEa9nCQo3HiP6+02YZW8Jc/Q3CzKo=
-----END CERTIFICATE-----`

const testKeyPEM = `-----BEGIN EC PRIVATE KEY-----
MHcCAQEEIL4AUQkoDEMen8Is8evemDj0eki6OB+mKD60JpytKfoUoAoGCCqGSM49
AwEHoUQDQgAEviR4gPMyOY/1e9v8QLIM24XlowWj74STkc9p2HXy652W/k70R9wV
hoamF3jXO25fc0hjtaqFD4rLPI1HpSgzKw==
-----END EC PRIVATE KEY-----`
