// B404 (v1.5.147) — the write-only contract for the two backup
// credentials, at the point where the form value meets the stored
// value.
//
// The DB round trip itself is covered by internal/backup (real SQLite
// DB); these tests pin the DECISION the handler makes, which is the
// half that can silently erase a credential: the form no longer
// prefills the secret, so an empty field must mean "keep", not
// "delete".
package admin

import (
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
)

func TestB404_MergeSubmittedSecret(t *testing.T) {
	cases := []struct {
		name   string
		form   url.Values
		field  string
		clear  string
		stored string
		want   string
	}{
		{
			name:   "empty field keeps the stored secret",
			form:   url.Values{},
			field:  "password",
			clear:  "clear_password",
			stored: "stored-pass",
			want:   "stored-pass",
		},
		{
			name:   "typed value replaces the stored secret",
			form:   url.Values{"password": {"new-pass"}},
			field:  "password",
			clear:  "clear_password",
			stored: "stored-pass",
			want:   "new-pass",
		},
		{
			name:   "clear flag deletes the stored secret",
			form:   url.Values{"clear_password": {"1"}},
			field:  "password",
			clear:  "clear_password",
			stored: "stored-pass",
			want:   "",
		},
		{
			name:   "typed value wins over the clear flag",
			form:   url.Values{"password": {"new-pass"}, "clear_password": {"1"}},
			field:  "password",
			clear:  "clear_password",
			stored: "stored-pass",
			want:   "new-pass",
		},
		{
			name:   "a clear flag value other than 1 is not a delete",
			form:   url.Values{"clear_password": {"0"}},
			field:  "password",
			clear:  "clear_password",
			stored: "stored-pass",
			want:   "stored-pass",
		},
		{
			name:   "no clear box at all (test path) can never delete",
			form:   url.Values{},
			field:  "s3_secret_key",
			clear:  "",
			stored: "stored-s3",
			want:   "stored-s3",
		},
		{
			name:   "nothing stored and nothing typed stays empty",
			form:   url.Values{},
			field:  "password",
			clear:  "clear_password",
			stored: "",
			want:   "",
		},
		{
			name:   "a secret made only of spaces is a real value",
			form:   url.Values{"password": {"   "}},
			field:  "password",
			clear:  "clear_password",
			stored: "stored-pass",
			want:   "   ",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			body := strings.NewReader(tc.form.Encode())
			r := httptest.NewRequest("POST", "/admin/backup/config", body)
			r.Header.Set("Content-Type", "application/x-www-form-urlencoded")
			if err := r.ParseForm(); err != nil {
				t.Fatalf("ParseForm: %v", err)
			}
			if got := mergeSubmittedSecret(r, tc.field, tc.clear, tc.stored); got != tc.want {
				t.Fatalf("mergeSubmittedSecret = %q, want %q", got, tc.want)
			}
		})
	}
}
