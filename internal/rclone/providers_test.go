// providers_test.go — guards the provider catalog invariant the add-remote
// wizard depends on: every option carries a non-nil Examples slice. The form
// reads `examples.length` per option, and a nil slice (dropped from JSON by
// omitempty) used to crash the whole page with "Cannot read properties of
// undefined (reading 'length')".
package rclone

import (
	"context"
	"os"
	"path/filepath"
	"runtime"
	"testing"
)

func TestProvidersNeverReturnsNilExamples(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("shell-script fake binary is POSIX-only")
	}

	dir := t.TempDir()
	fake := filepath.Join(dir, "rclone")
	// Run() prepends "--config <path>" to the args, so match the verb anywhere
	// in argv. Three option shapes exactly as rclone emits them: no Examples
	// key at all, an explicit empty array, and a populated one.
	script := `#!/bin/sh
case "$*" in
  *"config providers"*)
    cat <<'JSON'
[{"Name":"fake","Description":"fake provider","Options":[{"Name":"region","Required":true},{"Name":"acl","Examples":[]},{"Name":"loc","Examples":[{"Value":"eu","Help":"EU region"}]}]}]
JSON
    exit 0
    ;;
esac
echo "unexpected invocation: $*" >&2
exit 1
`
	if err := os.WriteFile(fake, []byte(script), 0o700); err != nil {
		t.Fatalf("write fake binary: %v", err)
	}
	InvalidateBinaryCache()
	t.Setenv(BinEnvVar, fake)
	t.Cleanup(func() {
		InvalidateBinaryCache()
		providerCache = nil // don't leak the fake catalog into other tests
		// ConfigFilePath() writes ./data/tools/rclone/empty.conf relative to CWD.
		_ = os.RemoveAll("./data")
	})

	provs, err := Providers(context.Background(), true)
	if err != nil {
		t.Fatalf("Providers: %v", err)
	}
	if len(provs) != 1 || provs[0].Name != "fake" {
		t.Fatalf("expected exactly the fake provider, got %+v", provs)
	}
	opts := provs[0].Options
	if len(opts) != 3 {
		t.Fatalf("expected 3 options, got %d", len(opts))
	}
	for _, o := range opts {
		if o.Examples == nil {
			t.Errorf("option %q: Examples must never be nil — the wizard reads .length on it", o.Name)
		}
	}
	if len(opts[0].Examples) != 0 {
		t.Errorf("missing Examples key must normalize to an empty slice, got %v", opts[0].Examples)
	}
	if len(opts[1].Examples) != 0 {
		t.Errorf("empty Examples must stay empty, got %v", opts[1].Examples)
	}
	if len(opts[2].Examples) != 1 || opts[2].Examples[0].Value != "eu" {
		t.Errorf("populated Examples must survive, got %v", opts[2].Examples)
	}
}
