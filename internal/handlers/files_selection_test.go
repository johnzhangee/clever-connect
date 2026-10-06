package handlers

// Tests for the directory listing's per-torrent virtual entries and the
// selection filter that drives them. When a user adds a torrent with only some
// files checked, only those files may appear in the file explorer: deselected
// files are never downloaded, so they have no file on disk and no archived S3
// object — listing them would create ghost entries with nothing behind them.
// Torrents added without the selection feature ("") keep the previous
// behaviour of listing every file.

import (
	"testing"
)

func TestTorrentSelectionFilter(t *testing.T) {
	cases := []struct {
		name         string
		selectedJSON string
		wantAll      bool
		wantWanted   map[int]bool
	}{
		{"no selection means every file", "", true, nil},
		{"legacy null means every file", "null", true, nil},
		{"unparsable means every file (matches the download funnel)", "not json", true, nil},
		{"empty array means nothing selected", "[]", false, map[int]bool{}},
		{"listed indices only", "[0,2]", false, map[int]bool{0: true, 2: true}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			all, wanted := torrentSelectionFilter(tc.selectedJSON)
			if all != tc.wantAll {
				t.Fatalf("all = %v, want %v", all, tc.wantAll)
			}
			if len(wanted) != len(tc.wantWanted) {
				t.Fatalf("wanted = %v, want %v", wanted, tc.wantWanted)
			}
			for idx := range tc.wantWanted {
				if !wanted[idx] {
					t.Fatalf("wanted = %v, missing index %d", wanted, idx)
				}
			}
		})
	}
}

func TestMergeTorrentVirtualEntries(t *testing.T) {
	refs := []torrentFileRef{
		{Path: "movie.mkv", Length: 1000},
		{Path: "subdir/trailer.mkv", Length: 100},
		{Path: "subdir/nested/sample.mkv", Length: 10},
	}
	const saveDir = "/data/downloads"

	t.Run("no selection lists every file and surfaced folders", func(t *testing.T) {
		virtual := make(map[string]FileItem)
		mergeTorrentVirtualEntries(virtual, saveDir, saveDir, refs, true, nil)

		movie, ok := virtual["movie.mkv"]
		if !ok || movie.IsDir || movie.Size != 1000 {
			t.Fatalf("movie.mkv entry = %+v, ok = %v", movie, ok)
		}
		dir, ok := virtual["subdir"]
		if !ok || !dir.IsDir {
			t.Fatalf("subdir entry = %+v, ok = %v", dir, ok)
		}
		if len(virtual) != 2 {
			t.Fatalf("virtual entries = %v, want movie.mkv and subdir", virtual)
		}
	})

	t.Run("partial selection hides deselected files and empty folders", func(t *testing.T) {
		virtual := make(map[string]FileItem)
		// Only the nested sample is checked — its top-level folder surfaces.
		mergeTorrentVirtualEntries(virtual, saveDir, saveDir, refs, false, map[int]bool{2: true})

		if _, ok := virtual["movie.mkv"]; ok {
			t.Fatal("deselected top-level file must not appear")
		}
		if _, ok := virtual["subdir/trailer.mkv"]; ok {
			t.Fatal("deselected nested file must not appear")
		}
		dir, ok := virtual["subdir"]
		if !ok || !dir.IsDir {
			t.Fatalf("folder of a selected file must surface: %+v, ok = %v", dir, ok)
		}
		if len(virtual) != 1 {
			t.Fatalf("virtual entries = %v, want only subdir", virtual)
		}
	})

	t.Run("explicit empty selection lists nothing", func(t *testing.T) {
		virtual := make(map[string]FileItem)
		mergeTorrentVirtualEntries(virtual, saveDir, saveDir, refs, false, map[int]bool{})
		if len(virtual) != 0 {
			t.Fatalf("virtual entries = %v, want none", virtual)
		}
	})
}
