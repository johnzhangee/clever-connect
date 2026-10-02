// remotes.go — DB-backed remote management: option parsing, credential
// handling, safe path assembly and the typed rclone operations built on Run.
package rclone

import (
	"context"
	"encoding/json"
	"fmt"
	"regexp"
	"sort"
	"strings"
	"time"

	"clever-connect/internal/models"
)

// remoteNameRe governs admin-chosen remote names. rclone remote names must be
// valid C identifiers after sanitising for env config variables, and this is
// the conservative subset accepted by this feature (lower-case start, letters,
// digits, underscores, dashes; <= 64 chars).
var remoteNameRe = regexp.MustCompile(`^[a-z0-9][a-z0-9_-]{0,63}$`)

// ValidRemoteName reports whether the remote name meets the rclone naming rules
// this engine enforces.
func ValidRemoteName(name string) bool { return remoteNameRe.MatchString(name) }

// envNameFor maps a remote name to the sanitised form used in env config keys:
// upper-case with "-" replaced by "_".
func envNameFor(name string) string {
	return strings.ToUpper(strings.ReplaceAll(name, "-", "_"))
}

// SecretMarker is the constant placeholder a masked secret value is replaced
// with in List APIs — and returned unchanged from the UI meaning "keep old".
const SecretMarker = "••••••••"

// BuiltIn secret substrings: option names that always hold credentials when the
// provider itself does not mark them as IsPassword (defensive double-net).
var secretNameHints = []string{
	"pass", "token", "secret", "key_file", "privatekey", "clientsecret",
	"apppassword", "app_password", "auth", "credential", "signingkey", "apikey",
	"api_key", "accesskey", "access_key", "sharedkey", "shared_key",
}

// ParseOptions decodes the JSON option object stored on a remote row.
func ParseOptions(remote *models.RcloneRemote) (map[string]string, error) {
	opts := map[string]string{}
	trimmed := strings.TrimSpace(remote.Options)
	if trimmed == "" {
		return opts, nil
	}
	if err := json.Unmarshal([]byte(trimmed), &opts); err != nil {
		return nil, fmt.Errorf("failed to parse stored remote options: %w", err)
	}
	return opts, nil
}

// RedactValues collects every live secret value of the remote for output
// scrubbing: values of options flagged secret on the row, merged with the
// provider's live IsPassword classification, merged with built-in name
// heuristics when neither source is available (test/binary-less resilience).
func RedactValues(remote *models.RcloneRemote) ([]string, error) {
	opts, err := ParseOptions(remote)
	if err != nil {
		return nil, err
	}
	secrets := map[string]bool{}
	for _, f := range remote.SecretFields {
		secrets[strings.ToLower(f)] = true
	}
	if providerSecrets, err := ProviderSecretFields(remote.Type); err == nil {
		for _, f := range providerSecrets {
			secrets[strings.ToLower(f)] = true
		}
	} else {
		// No provider metadata: mask on name heuristics so secrets are never
		// leaked just because the metadata source is unavailable.
		for k := range opts {
			lower := strings.ToLower(k)
			for _, hint := range secretNameHints {
				if strings.Contains(lower, hint) {
					secrets[lower] = true
					break
				}
			}
		}
	}
	var out []string
	for k, v := range opts {
		if v == "" {
			continue
		}
		if secrets[strings.ToLower(k)] {
			out = append(out, v)
		}
	}
	return out, nil
}

// BuildEnv creates the environment variable injection for one invocation: the
// remote's Type plus every set option becomes RCLONE_CONFIG_<NAME>_<OPTION>=<v>.
func BuildEnv(remote *models.RcloneRemote) (map[string]string, error) {
	opts, err := ParseOptions(remote)
	if err != nil {
		return nil, err
	}

	env := map[string]string{}
	prefix := "RCLONE_CONFIG_" + envNameFor(remote.Name) + "_"

	// The <type> field on its own env var defines the backend for this remote.
	env[prefix+"TYPE"] = strings.ToLower(remote.Type)

	for k, v := range opts {
		if v == "" {
			continue // leave unset options absent so rclone defaults apply
		}
		env[prefix+envOptionKey(k)] = v
	}
	return env, nil
}

// envOptionKey converts an option name ("client_secret") into the env key
// segment rclone expects (upper-case, '-' → '_').
func envOptionKey(option string) string {
	option = strings.ToLower(strings.TrimSpace(option))
	option = strings.ReplaceAll(option, "-", "_")
	return strings.ToUpper(option)
}

// forbiddenFlagChars are shell metacharacters that must never appear inside an
// extra flag token (every command/quote injection vector).
const forbiddenFlagChars = "`$\n\r\t;\\\"'|<>&(){}[]!~"

// ValidateExtraFlags splits a raw provider-configured flag string and confirms
// every strict "--flag value" token matches safe syntax. The validated list is
// returned trimmed; any dangerous input yields an error.
func ValidateExtraFlags(raw string) ([]string, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return nil, nil
	}

	fields := strings.Fields(raw)
	if len(fields)%2 != 0 {
		return nil, fmt.Errorf("extra flags must be pairs: --flag value (got %d token(s))", len(fields))
	}

	var parsed []string
	haveFlag := ""
	for _, tok := range fields {
		if haveFlag == "" {
			if !strings.HasPrefix(tok, "--") {
				return nil, fmt.Errorf("flags must begin with -- (got %q)", tok)
			}
			if strings.EqualFold(tok, "--config") {
				return nil, fmt.Errorf("--config is managed by the engine and cannot be overridden")
			}
			namePart := tok[len("--"):]
			if namePart == "" || strings.ContainsAny(namePart, forbiddenFlagChars) || strings.Contains(namePart, "--") {
				return nil, fmt.Errorf("malformed flag %q", tok)
			}
			haveFlag = tok
		} else {
			if strings.ContainsAny(tok, forbiddenFlagChars) {
				return nil, fmt.Errorf("flag %s has an unsafe value %q", haveFlag, tok)
			}
			parsed = append(parsed, haveFlag, tok)
			haveFlag = ""
		}
	}
	if haveFlag != "" {
		return nil, fmt.Errorf("flag %s is missing its value", haveFlag)
	}
	return parsed, nil
}

// joinRemotePath assembles "<Name>:<RootPrefix><dest>/<file>" with normalized
// slashes. dest may also already contain a path.
func joinRemotePath(remote *models.RcloneRemote, sub string) string {
	remoteName := remote.Name
	root := strings.ReplaceAll(strings.TrimSpace(remote.RootPrefix), "\\", "/")
	sub = strings.ReplaceAll(strings.TrimSpace(sub), "\\", "/")

	var parts []string
	if root != "" {
		parts = append(parts, strings.Trim(root, "/"))
	}
	if sub != "" {
		parts = append(parts, strings.Trim(sub, "/"))
	}
	if len(parts) == 0 {
		return remoteName + ":"
	}
	return remoteName + ":" + strings.Join(parts, "/")
}

// mkEnv assembles the invocation environment (options + secrets redaction).
func mkEnv(remote *models.RcloneRemote, extra map[string]string) (map[string]string, []string, error) {
	env, err := BuildEnv(remote)
	if err != nil {
		return nil, nil, err
	}
	for k, v := range extra {
		env[k] = v
	}
	redact, err := RedactValues(remote)
	if err != nil {
		return nil, nil, err
	}
	return env, redact, nil
}

// RemoteEntry is one item of a remote directory listing (lsjson).
type RemoteEntry struct {
	Name     string `json:"Name"`
	Path     string `json:"Path"`
	Size     int64  `json:"Size"`
	IsDir    bool   `json:"IsDir"`
	MimeType string `json:"MimeType"`
	ModTime  string `json:"ModTime"`
	ID       string `json:"ID"`
}

// ListRemoteDir issues `rclone lsjson` against remote:sub and returns entries.
// Sub is a path fragment relative to the remote RootPrefix; recursive flags
// a full single-shot listing of the subtree.
func ListRemoteDir(ctx context.Context, remote *models.RcloneRemote, sub string, recursive bool) ([]RemoteEntry, error) {
	target := joinRemotePath(remote, sub)
	env, redact, envErr := mkEnv(remote, nil)
	if envErr != nil {
		return nil, envErr
	}

	args := []string{"lsjson", target}
	if recursive {
		args = append(args, "--recursive")
	}

	res, err := Run(ctx, env, redact, 60*time.Second, args...)
	if err != nil {
		return nil, err
	}

	var entries []RemoteEntry
	out := strings.TrimSpace(res.Stdout)
	if out == "" {
		return entries, nil
	}
	if err := json.Unmarshal([]byte(out), &entries); err != nil {
		return nil, fmt.Errorf("failed to parse remote listing: %w", err)
	}
	sort.Slice(entries, func(i, j int) bool {
		if entries[i].IsDir != entries[j].IsDir {
			return entries[i].IsDir
		}
		return strings.ToLower(entries[i].Name) < strings.ToLower(entries[j].Name)
	})
	return entries, nil
}

// Stat runs `lsjson --stat` against a fully qualified remote path and returns
// the raw JSON plus the parsed essential fields. It is the post-upload source
// of ProviderMeta (document IDs, provider checksums, MIME types).
func Stat(ctx context.Context, remote *models.RcloneRemote, fullPath string) (raw string, entry *RemoteEntry, err error) {
	env, redact, envErr := mkEnv(remote, nil)
	if envErr != nil {
		return "", nil, envErr
	}
	res, err := Run(ctx, env, redact, 60*time.Second, "lsjson", fullPath, "--stat")
	if err != nil {
		return "", nil, err
	}
	out := strings.TrimSpace(res.Stdout)
	if out == "" {
		return "", nil, fmt.Errorf("provider stat returned no data")
	}
	var one RemoteEntry
	if err := json.Unmarshal([]byte(out), &one); err != nil {
		return "", nil, fmt.Errorf("failed to parse provider stat: %w", err)
	}
	return out, &one, nil
}

// TestReport carries the outcome of one remote connectivity test.
type TestReport struct {
	Ok        bool          `json:"ok"`
	Detail    string        `json:"detail"`
	RootPath  string        `json:"root_path"`
	Latency   time.Duration `json:"latency_ms"`
	Entries   int           `json:"entries"`
	CheckedAt time.Time     `json:"checked_at"`
}

// TestRemote probes connectivity by listing the remote root. err is returned
// only for infrastructure failures; logical provider errors are folded into
// the report for persistence on the remote row.
func TestRemote(ctx context.Context, remote *models.RcloneRemote) (*TestReport, error) {
	start := time.Now()
	report := &TestReport{CheckedAt: start, RootPath: joinRemotePath(remote, "")}

	entries, err := ListRemoteDir(ctx, remote, "", false)
	report.Latency = time.Since(start)
	if err != nil {
		report.Detail = err.Error()
		return report, nil
	}
	report.Ok = true
	report.Entries = len(entries)
	report.Detail = fmt.Sprintf("connection OK — root path %s lists %d entr%s", report.RootPath, len(entries), plural(len(entries)))
	return report, nil
}

func plural(n int) string {
	if n == 1 {
		return "y"
	}
	return "ies"
}

// UploadArgs packs the validated additional flags for one transfer run.
type UploadArgs struct {
	Args      []string // already validated --flag value pairs
	MakeLink  bool
	FetchStat bool
}

// UploadFile performs `rclone copyto srcLocal dst:...` for one file with
// sensible production hardening:
//
//   - --retries 3, --low-level-retries 10 ride out transient API failures
//   - --stats=0 keeps captured std stream free of periodic reposts
//   - transfers are copy-only; the local file always remains untouched
//     (eviction remains the S3/Cellar storage guard's responsibility)
//
// On success, ProviderMeta and PublicLink may additionally be fetched when
// requested via args.FetchStat / args.MakeLink.
func UploadFile(ctx context.Context, remote *models.RcloneRemote, localAbs string, destSub string, extra UploadArgs) (*models.RcloneUpload, error) {
	if _, err := EnsureBinary(ctx); err != nil {
		return nil, err
	}

	dest := joinRemotePath(remote, destSub)
	env, redact, envErr := mkEnv(remote, nil)
	if envErr != nil {
		return nil, envErr
	}

	cmdArgs := make([]string, 0, 8+len(extra.Args))
	cmdArgs = append(cmdArgs, "copyto", localAbs, dest)
	cmdArgs = append(cmdArgs, "--retries", "3", "--low-level-retries", "10", "--stats=0")
	cmdArgs = append(cmdArgs, extra.Args...)

	res, err := Run(ctx, env, redact, 0, cmdArgs...) // no hard cap: ctx governs
	if err != nil {
		return nil, err
	}

	return &models.RcloneUpload{
		RemotePath:    dest,
		ProviderMeta:  "",
		Status:        "success",
		TransferError: res.TrimmedErr(2),
	}, nil
}

// CreateLink issues `rclone link` against a fully qualified remote object and
// returns the public URL. Providers without public-link support surface
// *OptionalExitError so callers can flag it as a benign capability gap.
func CreateLink(ctx context.Context, remote *models.RcloneRemote, fullPath string) (string, error) {
	env, redact, envErr := mkEnv(remote, nil)
	if envErr != nil {
		return "", envErr
	}
	res, err := Run(ctx, env, redact, 60*time.Second, "link", fullPath)
	if err != nil {
		lower := strings.ToLower(res.ErrTail())
		if strings.Contains(lower, "doesn't support") || strings.Contains(lower, "does not support") || strings.Contains(lower, "not supported") {
			return "", &OptionalExitError{Op: "link", Detail: res.ErrTail()}
		}
		return "", err
	}
	urlStr := strings.TrimSpace(res.Stdout)
	// rclone `link` prints the URL followed by a flag-value legend line.
	if idx := strings.IndexByte(urlStr, '\n'); idx > 0 {
		urlStr = strings.TrimSpace(urlStr[:idx])
	}
	if urlStr == "" {
		return "", fmt.Errorf("provider returned an empty public link")
	}
	return urlStr, nil
}

// DeleteObject removes one file from the remote (used when the admin deletes
// an upload history row with purge semantics).
func DeleteObject(ctx context.Context, remote *models.RcloneRemote, fullPath string) error {
	env, redact, envErr := mkEnv(remote, nil)
	if envErr != nil {
		return envErr
	}
	_, err := Run(ctx, env, redact, 2*time.Minute, "deletefile", fullPath)
	return err
}
