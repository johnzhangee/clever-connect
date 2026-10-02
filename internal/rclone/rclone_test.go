// rclone_test.go — unit tests for the pure helpers of the cloud storage engine
// (no DB and no rclone binary required).
package rclone

import (
	"reflect"
	"testing"

	"clever-connect/internal/models"
)

func TestEnvNameFor(t *testing.T) {
	cases := map[string]string{
		"mydrive":   "MYDRIVE",
		"my-drive":  "MY_DRIVE",
		"gdrive_v2": "GDRIVE_V2",
	}
	for in, want := range cases {
		if got := envNameFor(in); got != want {
			t.Errorf("envNameFor(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestEnvOptionKey(t *testing.T) {
	cases := map[string]string{
		"client_secret":  "CLIENT_SECRET",
		"RootFolderID":   "ROOTFOLDERID",
		"root-folder-id": "ROOT_FOLDER_ID",
	}
	for in, want := range cases {
		if got := envOptionKey(in); got != want {
			t.Errorf("envOptionKey(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestValidRemoteName(t *testing.T) {
	valid := []string{"drive", "s3-main", "gdrive_v2", "a0", "x" + string(make([]byte, 0))}
	for _, v := range valid {
		if !ValidRemoteName(v) {
			t.Errorf("ValidRemoteName(%q) should be true", v)
		}
	}
	invalid := []string{"", "-lead", "_lead", "Upper", "has space", "has.dot", "häs", "way-too-" + string(make([]byte, 60))}
	for _, v := range invalid {
		if ValidRemoteName(v) {
			t.Errorf("ValidRemoteName(%q) should be false", v)
		}
	}
}

func TestBuildEnv(t *testing.T) {
	remote := &models.RcloneRemote{
		Name:    "my-drive",
		Type:    "SFTP",
		Options: `{"host":"example.com","user":"root","pass":"s3cret","empty":""}`,
	}
	env, err := BuildEnv(remote)
	if err != nil {
		t.Fatalf("BuildEnv: %v", err)
	}
	want := map[string]string{
		"RCLONE_CONFIG_MY_DRIVE_TYPE": "sftp",
		"RCLONE_CONFIG_MY_DRIVE_HOST": "example.com",
		"RCLONE_CONFIG_MY_DRIVE_USER": "root",
		"RCLONE_CONFIG_MY_DRIVE_PASS": "s3cret",
	}
	for k, v := range want {
		if env[k] != v {
			t.Errorf("env[%s] = %q, want %q", k, env[k], v)
		}
	}
	if _, ok := env["RCLONE_CONFIG_MY_DRIVE_EMPTY"]; ok {
		t.Errorf("empty options must be omitted, got env[%s]", "RCLONE_CONFIG_MY_DRIVE_EMPTY")
	}
}

func TestParseOptionsBadJSON(t *testing.T) {
	remote := &models.RcloneRemote{Options: "{not json"}
	if _, err := ParseOptions(remote); err == nil {
		t.Fatal("expected error for malformed options JSON")
	}
}

func TestRedactValuesMasksSecrets(t *testing.T) {
	remote := &models.RcloneRemote{
		Options:      `{"pass":"pw123","host":"example.com"}`,
		SecretFields: models.StringArray{"pass"},
	}
	vals, err := RedactValues(remote)
	if err != nil {
		t.Fatalf("RedactValues: %v", err)
	}
	if !reflect.DeepEqual(vals, []string{"pw123"}) {
		t.Errorf("RedactValues = %v, want [pw123]", vals)
	}
}

func TestSecretMarkerStable(t *testing.T) {
	if SecretMarker == "" || len(SecretMarker) < 4 {
		t.Fatalf("SecretMarker must be stable and non-trivial: %q", SecretMarker)
	}
}

func TestValidateExtraFlags(t *testing.T) {
	got, err := ValidateExtraFlags("--transfers 4 --sftp-idle-timeout 10")
	if err != nil {
		t.Fatalf("ValidateExtraFlags(valid): %v", err)
	}
	if !reflect.DeepEqual(got, []string{"--transfers", "4", "--sftp-idle-timeout", "10"}) {
		t.Errorf("parsed flags = %v", got)
	}

	if _, err := ValidateExtraFlags(""); err != nil {
		t.Errorf("empty flags must parse cleanly: %v", err)
	}

	bad := []string{
		"--config /etc/passwd", // engine-managed flag
		"--transfers",          // dangling value
		"transfers 4",          // missing leading --
		"--x `rm -rf /`",       // backtick injection
		"--x $(whoami)",        // command substitution
		"--x /tmp/a;rm -rf",    // semicolon chaining
		"--x |cat /etc/shadow", // pipe metachar
		"--x q'w",              // quote injection
		"--x <redirect",        // redirection
		"--x-val",              // flag acting as value
	}
	for _, in := range bad {
		if _, err := ValidateExtraFlags(in); err == nil {
			t.Errorf("ValidateExtraFlags(%q) should fail", in)
		}
	}
}

func TestJoinRemotePath(t *testing.T) {
	cases := []struct {
		root, sub, want string
	}{
		{"", "", "drive:"},
		{"", "file.mkv", "drive:file.mkv"},
		{"backups/", "", "drive:backups"},
		{"backups/", "2024/file.mkv", "drive:backups/2024/file.mkv"},
		{"/media//", "movies/movie.mkv", "drive:media/movies/movie.mkv"},
		{"a\\b", "c/d.mkv", "drive:a/b/c/d.mkv"},
	}
	for _, c := range cases {
		remote := &models.RcloneRemote{Name: "drive", RootPrefix: c.root}
		if got := joinRemotePath(remote, c.sub); got != c.want {
			t.Errorf("joinRemotePath(root=%q,sub=%q) = %q, want %q", c.root, c.sub, got, c.want)
		}
	}
}
