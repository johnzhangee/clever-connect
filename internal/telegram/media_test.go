package telegram

import (
	"context"
	"encoding/binary"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/gotd/td/bin"
	"github.com/gotd/td/telegram"
	"github.com/gotd/td/tgerr"
)

// writeTestBox writes a single ISO/IEC 14496-12 box (size + type + payload).
func writeTestBox(f *os.File, typ string, payload []byte) {
	var hdr [8]byte
	binary.BigEndian.PutUint32(hdr[0:4], uint32(8+len(payload)))
	copy(hdr[4:8], typ)
	_, _ = f.Write(hdr[:])
	_, _ = f.Write(payload)
}

func TestMP4IsFaststart(t *testing.T) {
	tests := []struct {
		name      string
		boxes     []string
		faststart bool
		wantErr   bool
	}{
		{"moov before mdat", []string{"ftyp", "moov", "mdat"}, true, false},
		{"moov after mdat", []string{"ftyp", "mdat", "moov"}, false, false},
		{"moov before large mdat", []string{"ftyp", "free", "moov", "mdat"}, true, false},
		{"no moov", []string{"ftyp", "mdat"}, false, false},
		{"empty file", nil, false, true},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			f, err := os.CreateTemp(t.TempDir(), "test-*.mp4")
			if err != nil {
				t.Fatal(err)
			}
			for _, b := range tc.boxes {
				writeTestBox(f, b, []byte("payload-payload-payload"))
			}
			f.Close()

			got, err := mp4IsFaststart(f.Name())
			if tc.wantErr {
				if err == nil {
					t.Fatalf("expected error, got faststart=%v", got)
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if got != tc.faststart {
				t.Fatalf("faststart = %v, want %v", got, tc.faststart)
			}
		})
	}
}

func TestExtractMigrateDC(t *testing.T) {
	tests := []struct {
		name string
		err  error
		want int
		ok   bool
	}{
		{
			"plain migrate error",
			tgerr.New(303, "FILE_MIGRATE_4"),
			4, true,
		},
		{
			"wrapped migrate error",
			fmt.Errorf("parallel download failed (threads=%d): %w", 8,
				fmt.Errorf("get file: %w", tgerr.New(303, "FILE_MIGRATE_2"))),
			2, true,
		},
		{
			"non-migrate rpc error",
			fmt.Errorf("wrapped: %w", tgerr.New(400, "LOCATION_INVALID")),
			0, false,
		},
		{
			"plain error",
			fmt.Errorf("dial tcp: connection refused"),
			0, false,
		},
		{
			"nil error",
			nil,
			0, false,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got, ok := extractMigrateDC(tc.err)
			if ok != tc.ok || got != tc.want {
				t.Fatalf("extractMigrateDC() = (%d, %v), want (%d, %v)", got, ok, tc.want, tc.ok)
			}
		})
	}
}

func TestCalculateOptimalThreadsLadder(t *testing.T) {
	const (
		MB = 1024 * 1024
		KB = 1024
		GB = 1024 * MB
	)

	for _, tc := range []struct {
		size int64
		want int
	}{
		{1 * KB, 2},
		{49 * MB, 2},
		{100 * MB, 4},
		{400 * MB, 8},
		{700 * MB, 12},
		{1*GB + 5*MB, 14},
		{5 * GB, 16},
	} {
		if got := calculateOptimalThreads(tc.size); got != tc.want {
			t.Fatalf("calculateOptimalThreads(%d) = %d, want %d", tc.size, got, tc.want)
		}
	}
	// Threads must never exceed the connection pool ceiling.
	if got := calculateOptimalThreads(9 * GB); got > downloadPoolMaxConns {
		t.Fatalf("threads exceed the connection pool ceiling: %d > %d", got, downloadPoolMaxConns)
	}
}

// ffmpegAvailable reports whether ffmpeg is installed so the live remux tests
// can run (they are skipped on hosts without ffmpeg).
func ffmpegAvailable() bool {
	_, err := exec.LookPath("ffmpeg")
	return err == nil
}

// genTestVideo creates a tiny H.264 video with the given extra output flags.
func genTestVideo(t *testing.T, outPath string, extraArgs ...string) {
	t.Helper()
	args := append([]string{
		"-y", "-loglevel", "error",
		"-f", "lavfi", "-i", "testsrc=duration=0.4:size=96x64:rate=10",
		"-c:v", "libx264", "-pix_fmt", "yuv420p",
	}, extraArgs...)
	args = append(args, outPath)
	cmd := exec.Command("ffmpeg", args...)
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Skipf("ffmpeg could not generate test video: %v: %s", err, out)
	}
}

func TestPreparePlayableMediaFaststart(t *testing.T) {
	if !ffmpegAvailable() {
		t.Skip("ffmpeg not installed")
	}

	dir := t.TempDir()

	fast := filepath.Join(dir, "already-fast.mp4")
	genTestVideo(t, fast, "-movflags", "+faststart")

	slow := filepath.Join(dir, "moov-at-end.mp4")
	genTestVideo(t, slow) // default muxer writes moov at the end

	// Sanity: the generated files must be detected as expected.
	if ok, err := mp4IsFaststart(fast); err != nil || !ok {
		t.Fatalf("expected generated file to be faststart (ok=%v err=%v)", ok, err)
	}
	if ok, err := mp4IsFaststart(slow); err != nil || ok {
		t.Fatalf("expected generated file to NOT be faststart (ok=%v err=%v)", ok, err)
	}

	// A faststart MP4 must pass through untouched.
	path, name, cleanup := preparePlayableMedia(fast, "already-fast.mp4", nil)
	if path != fast || name != "already-fast.mp4" || cleanup != nil {
		t.Fatalf("faststart file should not be remuxed: path=%q name=%q cleanup set", path, name)
	}

	// A non-faststart MP4 must be remuxed into a playable, faststart MP4.
	path, name, cleanup = preparePlayableMedia(slow, "moov-at-end.mp4", nil)
	if path == slow {
		t.Fatal("non-faststart MP4 should have been remuxed")
	}
	if name != "moov-at-end.mp4" {
		t.Fatalf("remuxed display name = %q, want %q", name, "moov-at-end.mp4")
	}
	if cleanup == nil {
		t.Fatal("cleanup func expected for the remuxed temp file")
	}
	if ok, err := mp4IsFaststart(path); err != nil || !ok {
		t.Fatalf("remuxed file should be faststart (ok=%v err=%v)", ok, err)
	}
	st, err := os.Stat(path)
	if err != nil || st.Size() == 0 {
		t.Fatalf("remuxed file missing or empty (err=%v)", err)
	}
	cleanup()
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Fatalf("cleanup did not remove the remuxed temp file (err=%v)", err)
	}
}

func TestPreparePlayableMediaMKVToMP4(t *testing.T) {
	if !ffmpegAvailable() {
		t.Skip("ffmpeg not installed")
	}

	dir := t.TempDir()
	mkv := filepath.Join(dir, "movie.mkv")
	// H.264 in a Matroska container is directly remuxable to MP4.
	genTestVideo(t, mkv, "-f", "matroska")

	path, name, cleanup := preparePlayableMedia(mkv, "movie.mkv", nil)
	if path == mkv {
		t.Fatal("MKV should have been remuxed to MP4")
	}
	if !strings.HasSuffix(name, ".mp4") {
		t.Fatalf("remuxed display name = %q, want .mp4 suffix", name)
	}
	if ok, err := mp4IsFaststart(path); err != nil || !ok {
		t.Fatalf("remuxed MKV should be a faststart MP4 (ok=%v err=%v)", ok, err)
	}
	cleanup()
}

func TestPreparePlayableMediaLeavesOtherFormatsAlone(t *testing.T) {
	dir := t.TempDir()

	for _, name := range []string{"song.mp3", "photo.jpg", "archive.zip", "doc.pdf"} {
		p := filepath.Join(dir, name)
		if err := os.WriteFile(p, []byte("not really media"), 0644); err != nil {
			t.Fatal(err)
		}
		path, displayName, cleanup := preparePlayableMedia(p, name, nil)
		if path != p || displayName != name || cleanup != nil {
			t.Fatalf("%s should not be touched (path=%q name=%q)", name, path, displayName)
		}
	}
}

func TestPreparePlayableMediaFallsBackOnGarbageMP4(t *testing.T) {
	if !ffmpegAvailable() {
		t.Skip("ffmpeg not installed")
	}

	dir := t.TempDir()
	garbage := filepath.Join(dir, "broken.mp4")
	// A file with a moov-after-mdat box layout but garbage payloads — ffmpeg
	// must fail and the original file must be uploaded untouched.
	f, err := os.Create(garbage)
	if err != nil {
		t.Fatal(err)
	}
	writeTestBox(f, "ftyp", []byte("isom"))
	writeTestBox(f, "mdat", []byte(strings.Repeat("x", 512)))
	writeTestBox(f, "moov", []byte(strings.Repeat("y", 64)))
	f.Close()

	path, name, cleanup := preparePlayableMedia(garbage, "broken.mp4", nil)
	if path != garbage || name != "broken.mp4" || cleanup != nil {
		t.Fatal("invalid MP4 must fall back to the original file")
	}
}

func TestIsPoolFailure(t *testing.T) {
	// The exact error shapes gotd produces when a connection pool is dead or
	// stale (e.g. its owning client was closed after an engine restart).
	poolFailures := []string{
		// Observed in production logs: stale cached upload pool.
		"parallel upload failed: upload part: send upload part 0 RPC: acquire connection: DC closed: context canceled",
		"acquire connection: DC closed: context deadline exceeded",
		"upload part: send upload part 3 RPC: invoke pool: engine forcibly closed",
		"invoke pool: write: connection reset by peer",
		"DC is closed",
		"create DC 2 pool: client already closed",
		"engine forcibly closed: context canceled",
	}
	for _, msg := range poolFailures {
		if !isPoolFailure(fmt.Errorf("%s", msg)) {
			t.Errorf("isPoolFailure(%q) = false, want true", msg)
		}
	}

	// Ordinary errors (including a plain cancelled *request* context) must NOT
	// be mistaken for pool failures — retrying those wastes a full re-upload.
	nonPoolFailures := []string{
		"parallel upload failed: context canceled",
		"file upload failed: FLOOD_WAIT_420",
		"open /tmp/file.bin: no such file or directory",
		"upload part: send upload part 0 RPC: FILE_PARTS_INVALID",
	}
	for _, msg := range nonPoolFailures {
		if isPoolFailure(fmt.Errorf("%s", msg)) {
			t.Errorf("isPoolFailure(%q) = true, want false", msg)
		}
	}

	if isPoolFailure(nil) {
		t.Error("isPoolFailure(nil) = true, want false")
	}
}

func TestPoolCachesAreKeyedByClient(t *testing.T) {
	// telegram.NewClient does not dial anything, so distinct instances are
	// safe to use as cache keys in tests.
	c1 := telegram.NewClient(2040, "b18441a1ff607e10a989891a5624e0d4", telegram.Options{})
	c2 := telegram.NewClient(2040, "b18441a1ff607e10a989891a5624e0d4", telegram.Options{})

	// Upload pools: a pool stored for one client must not be served to
	// another, and eviction must be isolated per client.
	uploadPools.Store(c1, fakeInvoker{})
	if _, ok := uploadPools.Load(c2); ok {
		t.Fatal("upload pool cached for client #1 leaked into client #2")
	}
	resetUploadPool(c1)
	if _, ok := uploadPools.Load(c1); ok {
		t.Fatal("resetUploadPool did not evict client #1's pool")
	}

	// Download pools: same, per (client, DC) pair.
	dcDownloadPools.Store(dcPoolKey{client: c1, dc: 4}, fakeInvoker{})
	if _, ok := dcDownloadPools.Load(dcPoolKey{client: c2, dc: 4}); ok {
		t.Fatal("DC pool cached for client #1 leaked into client #2")
	}
	// Same client, different DC must be a distinct entry.
	if _, ok := dcDownloadPools.Load(dcPoolKey{client: c1, dc: 5}); ok {
		t.Fatal("DC 4 pool leaked into DC 5 entry")
	}
	evictDCPool(c1, 4)
	if _, ok := dcDownloadPools.Load(dcPoolKey{client: c1, dc: 4}); ok {
		t.Fatal("evictDCPool did not evict the (client #1, DC 4) pool")
	}
}

// fakeInvoker is a no-op stand-in for a pool entry in cache tests.
type fakeInvoker struct{}

func (fakeInvoker) Invoke(_ context.Context, _ bin.Encoder, _ bin.Decoder) error { return nil }
func (fakeInvoker) Close() error                                                 { return nil }
