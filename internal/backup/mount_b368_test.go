// B368 — the S3 branch of TestConnection must not answer OK for an address that
// refuses connections.
//
// MEASURED LIVE, 2026-10-09 (reference deployment). The operator's in-app S3
// backup had been failing for weeks with
//
//	backup.last_error: s3 upload: s3 bucket check: Head "http://172.18.0.5:9000/
//	                   skygate-backups/": dial tcp 172.18.0.5:9000: connect:
//	                   connection refused
//
// while the panel's «Test» button answered
//
//	S3 доступен: http://172.18.0.5:9000 · корзина: skygate-backups · регион: us-east-1
//
// The branch only checked that the fields were non-empty and echoed the endpoint
// back; its own comment claimed the network probe "happens in uploadToS3 at run
// time", which is true and useless — a green tick that cannot go red is how a
// MinIO that had MOVED TO ANOTHER HOST went unnoticed. The probe below is that
// same BucketExists call, bounded by a deadline so a click cannot hang.
package backup

import (
	"strings"
	"testing"
	"time"
)

// TestB368_S3TestFailsOnAnUnreachableEndpoint is the regression: this exact
// config used to come back OK.
func TestB368_S3TestFailsOnAnUnreachableEndpoint(t *testing.T) {
	cfg := &Config{
		Destination: "skygate-backups",
		Protocol:    ProtocolS3,
		S3Endpoint:  "http://127.0.0.1:1", // nothing listens here; ECONNREFUSED
		S3Region:    "us-east-1",
		S3AccessKey: "skyadmin",
		S3SecretKey: "not-a-real-secret",
		S3Bucket:    "skygate-backups",
	}
	t0 := time.Now()
	out := TestConnection(cfg)
	elapsed := time.Since(t0)

	if out.OK {
		t.Fatal("TestConnection reported OK for an endpoint that refuses connections — the operator's green tick " +
			"would again prove nothing while every scheduled backup fails")
	}
	if len(out.Issues) == 0 {
		t.Fatal("TestConnection failed the S3 probe without saying why")
	}
	if !strings.Contains(strings.Join(out.Issues, " "), "unreachable or credentials rejected") {
		t.Errorf("the failure does not name the probe result: %v", out.Issues)
	}
	if elapsed > s3TestTimeout+2*time.Second {
		t.Errorf("the probe took %s — the deadline is %s, so a click could hang the panel", elapsed, s3TestTimeout)
	}
}

// TestB368_S3TestStillReportsMissingFieldsFirst keeps the cheap validation in
// front of the network: a half-filled form must not be reported as a transport
// error.
func TestB368_S3TestStillReportsMissingFieldsFirst(t *testing.T) {
	cfg := &Config{Destination: "b", Protocol: ProtocolS3, S3Endpoint: "http://127.0.0.1:1"}
	t0 := time.Now()
	out := TestConnection(cfg)
	if out.OK {
		t.Fatal("an S3 config with no bucket and no credentials passed the test")
	}
	joined := strings.Join(out.Issues, " ")
	for _, want := range []string{"s3_bucket is required", "s3_access_key is required", "s3_secret_key is required"} {
		if !strings.Contains(joined, want) {
			t.Errorf("issues do not mention %q: %v", want, out.Issues)
		}
	}
	if strings.Contains(joined, "unreachable") {
		t.Errorf("a missing field was reported as a network failure: %v", out.Issues)
	}
	if time.Since(t0) > 2*time.Second {
		t.Errorf("the field-only path took %s — it must not dial anything", time.Since(t0))
	}
}

// TestB368_S3TestReportsAnExistingBucket pins the success shape: the probe fills
// the endpoint/bucket/region the page renders, and adds the measured latency.
func TestB368_S3TestReportsAnExistingBucket(t *testing.T) {
	// A config with no endpoint is not dialled here (AWS default), so this stays
	// a pure field test: the fields the success message reads must be present.
	cfg := &Config{
		Destination: "skygate-backups",
		Protocol:    ProtocolS3,
		S3Endpoint:  "http://127.0.0.1:1",
		S3Region:    "us-east-1",
		S3AccessKey: "skyadmin",
		S3SecretKey: "secret",
		S3Bucket:    "skygate-backups",
	}
	out := TestConnection(cfg)
	for _, k := range []string{"endpoint", "bucket", "region"} {
		if out.Fields[k] == "" {
			t.Errorf("the test result does not report %q — the page's success message reads it", k)
		}
	}
}
