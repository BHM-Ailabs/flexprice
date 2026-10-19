package searchattr

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// Exercise provisioning without a Temporal server or infrastructure writes.
func TestExplicitBootstrap(t *testing.T) {
	for _, tc := range []struct {
		name, state, fail string
		wantError         bool
		wantCreates       int
	}{
		{"fresh namespace", "", "", false, 5},
		{"existing contract", "SubscriptionID Keyword\nTenantID Keyword\nEnvironmentID Keyword\nFailingActivity Keyword\nFailureReason Keyword\n", "", false, 0},
		{"wrong type", "TenantID Int\n", "", true, 1},
		{"registration denied", "", "1", true, 1},
	} {
		t.Run(tc.name, func(t *testing.T) {
			dir := t.TempDir()
			state, calls := filepath.Join(dir, "state"), filepath.Join(dir, "calls")
			if err := os.WriteFile(state, []byte(tc.state), 0600); err != nil {
				t.Fatal(err)
			}
			fake := `#!/bin/sh
set -eu
case "$*" in
  *"search-attribute list"*)
    printf '{"customAttributes":{'
    sep=''
    while read -r name kind; do
      [ -n "$name" ] || continue
      printf '%s"%s":"%s"' "$sep" "$name" "$kind"
      sep=,
    done < "$STATE"
    printf '}}\n'
    ;;
  *"search-attribute create"*)
    printf 'create\n' >> "$CALLS"
    [ -z "$FAIL_CREATE" ] || exit 1
    while [ "$1" != '--name' ]; do shift; done
    printf '%s Keyword\n' "$2" >> "$STATE"
    ;;
  *) exit 9 ;;
esac
`
			if err := os.WriteFile(filepath.Join(dir, "temporal"), []byte(fake), 0700); err != nil {
				t.Fatal(err)
			}
			cmd := exec.Command("sh", "../../ee/infrastructure/helm/flexprice/files/bootstrap-temporal-search-attributes.sh")
			cmd.Env = append(os.Environ(), "PATH="+dir+":"+os.Getenv("PATH"), "STATE="+state, "CALLS="+calls, "FAIL_CREATE="+tc.fail, "TEMPORAL_ADDRESS=local-only:7233", "TEMPORAL_NAMESPACE=test")
			out, err := cmd.CombinedOutput()
			if (err != nil) != tc.wantError {
				t.Fatalf("err=%v output=%s", err, out)
			}
			b, _ := os.ReadFile(calls)
			if got := strings.Count(string(b), "create\n"); got != tc.wantCreates {
				t.Fatalf("creates=%d want=%d", got, tc.wantCreates)
			}
		})
	}
}
