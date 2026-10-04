package torrent

import (
	"testing"

	"clever-connect/internal/models"
)

// TestShouldMigrateToDirectS3 verifies the resume-time migration policy:
// legacy in-flight jobs switch to the direct-to-S3 backend (their bytes must
// stop landing on the small instance disk), while jobs already stored in S3
// or finished keep their mode. The feature gate must veto every migration.
func TestShouldMigrateToDirectS3(t *testing.T) {
	cases := []struct {
		name string
		job  models.TorrentJob
		want bool
	}{
		{"already direct: keep backend", models.TorrentJob{DirectS3: true, Status: "downloading"}, false},
		{"offloaded: data lives in S3", models.TorrentJob{Status: "seeding", OffloadStatus: "offloaded"}, false},
		{"completed without ledger: not re-added", models.TorrentJob{Status: "completed"}, false},
		{"downloading legacy", models.TorrentJob{Status: "downloading"}, true},
		{"paused legacy", models.TorrentJob{Status: "paused"}, true},
		{"seeding legacy", models.TorrentJob{Status: "seeding"}, true},
		{"fetching metadata (empty status)", models.TorrentJob{}, true},
	}
	for _, tc := range cases {
		if got := shouldMigrateToDirectS3(&tc.job, true); got != tc.want {
			t.Errorf("%s: got %v, want %v", tc.name, got, tc.want)
		}
	}
	for _, tc := range cases {
		if shouldMigrateToDirectS3(&tc.job, false) {
			t.Errorf("%s: must never migrate while direct mode is disabled", tc.name)
		}
	}
}
