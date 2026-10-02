// providers.go — `rclone config providers` metadata used by the admin UI's
// dynamic per-provider form, including OAuth detection and IsPassword values.
package rclone

import (
	"context"
	"encoding/json"
	"fmt"
	"sort"
	"strings"
	"sync"
	"time"

	"clever-connect/internal/logger"
	"clever-connect/internal/models"
)

// RcloneOption describes one provider option from `rclone config providers`.
type RcloneOption struct {
	Name       string          `json:"Name"`
	Help       string          `json:"Help"`
	Provider   string          `json:"Provider,omitempty"`
	Default    json.RawMessage `json:"Default,omitempty"`
	Examples   []Example       `json:"Examples,omitempty"`
	ShortOpt   string          `json:"ShortOpt,omitempty"`
	Required   bool            `json:"Required"`
	IsPassword bool            `json:"IsPassword"`
	NoPrefix   bool            `json:"NoPrefix"`
	Advanced   bool            `json:"Advanced"`
}

// Example is an accepted option value pair shown to admins in the form UI.
type Example struct {
	Value string `json:"Value"`
	Help  string `json:"Help"`
}

// RcloneProvider describes one provider (backend) entry.
type RcloneProvider struct {
	Name        string         `json:"Name"`
	Description string         `json:"Description"`
	Prefix      string         `json:"Prefix,omitempty"`
	Options     []RcloneOption `json:"Options"`
	// OAuth marks providers using an OAuth-style token flow (help panel will
	// guide the admin through `rclone authorize` on their own machine).
	HasOAuth bool `json:"HasOAuth"`
}

var (
	providerCache     []RcloneProvider
	providerCacheMtx  sync.Mutex
	providerFetchedAt time.Time
)

const providerCacheTTL = time.Hour

// Providers returns the full provider index, backed by an in-memory cache.
func Providers(ctx context.Context, forceRefresh bool) ([]RcloneProvider, error) {
	providerCacheMtx.Lock()
	defer providerCacheMtx.Unlock()

	if len(providerCache) > 0 && !forceRefresh && time.Since(providerFetchedAt) < providerCacheTTL {
		return providerCache, nil
	}

	provs, err := fetchProviders(ctx)
	if err != nil {
		if len(providerCache) > 0 {
			// A stale cache is better than nothing when the subprocess
			// transiently fails (e.g. mid binary swap).
			logger.Warn("Rclone", "provider refresh failed — serving cached index", "error", err)
			return providerCache, nil
		}
		return nil, err
	}
	providerCache = provs
	providerFetchedAt = time.Now()
	return providerCache, nil
}

// providerHasOAuth detects OAuth-style providers: they define a "token" option
// plus client_id/client_secret, or an "auth_url" option.
func providerHasOAuth(p *RcloneProvider) bool {
	byName := map[string]bool{}
	for _, o := range p.Options {
		byName[strings.ToLower(o.Name)] = true
	}
	if byName["token"] && (byName["client_id"] || byName["client_secret"] || byName["auth_url"]) {
		return true
	}
	return byName["auth_url"]
}

// fetchProviders shells out to `rclone config providers` and embeds the full
// option schema for the admin form.
func fetchProviders(ctx context.Context) ([]RcloneProvider, error) {
	if _, err := ResolveBinary(); err != nil {
		return nil, err
	}

	res, err := Run(ctx, nil, nil, 120*time.Second, "config", "providers")
	if err != nil {
		return nil, err
	}

	var provs []RcloneProvider
	body := strings.TrimSpace(res.Stdout)
	if body == "" || body[0] != '[' {
		return nil, fmt.Errorf("unexpected rclone config providers output (not JSON array)")
	}
	if err := json.Unmarshal([]byte(body), &provs); err != nil {
		return nil, fmt.Errorf("failed to parse rclone providers output: %w", err)
	}

	sort.Slice(provs, func(i, j int) bool {
		return strings.ToLower(provs[i].Name) < strings.ToLower(provs[j].Name)
	})
	for i := range provs {
		provs[i].HasOAuth = providerHasOAuth(&provs[i])
	}
	return provs, nil
}

// FindProvider locates one provider by its rclone type name.
func FindProvider(ctx context.Context, name string) (*RcloneProvider, error) {
	provs, err := Providers(ctx, false)
	if err != nil {
		return nil, err
	}
	lowerWant := strings.ToLower(strings.TrimSpace(name))
	for i := range provs {
		if strings.ToLower(provs[i].Name) == lowerWant {
			return &provs[i], nil
		}
	}
	return nil, fmt.Errorf("unknown rclone provider type %q", name)
}

// ProviderSecretFields returns the option names flagged as passwords for a
// provider type, falling back to the built-in name heuristics.
func ProviderSecretFields(providerType string) ([]string, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()

	p, err := FindProvider(ctx, providerType)
	if err != nil {
		return nil, err
	}

	seen := map[string]bool{}
	var out []string
	for _, o := range p.Options {
		lower := strings.ToLower(o.Name)
		if o.IsPassword && !seen[lower] {
			seen[lower] = true
			out = append(out, lower)
			continue
		}
		for _, hint := range secretNameHints {
			if strings.Contains(lower, hint) && !seen[lower] {
				seen[lower] = true
				out = append(out, lower)
				break
			}
		}
	}
	return out, nil
}

// ComputeSecretFields derives the definitive secret option list for a
// provider type: provider-flagged IsPassword options plus name heuristics.
// Unresolvable provider metadata yields an empty list — runtime redaction then
// relies on the live provider lookup, which sees the same failure and no-ops
// only when the engine could not run anything in the first place.
func ComputeSecretFields(providerType string) models.StringArray {
	fields, err := ProviderSecretFields(providerType)
	if err != nil {
		return models.StringArray{}
	}
	return fields
}
