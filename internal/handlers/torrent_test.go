package handlers

// Tests for the AddTorrent handler's multi-file upload support: every .torrent
// posted under the "file" form field is processed and added one by one, a
// batch never aborts on the first failure, a single-file request keeps the
// original response shape, and the send-to-telegram checkbox is forwarded into
// every manager add call.

import (
	"bytes"
	"encoding/json"
	"fmt"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"clever-connect/internal/config"

	"github.com/gin-gonic/gin"
)

func TestAddTorrentFileUpload(t *testing.T) {
	gin.SetMode(gin.TestMode)
	t.Chdir(t.TempDir()) // isolates the ./data/manager/temp the handler writes to

	// Stubbed manager seam: records every add call as
	// "name|saveDir|select|sendToTelegram" and fails files whose name starts
	// with "bad".
	var calls []string
	adder := func(path, saveDir string, selectFiles, sendToTelegram bool) (string, error) {
		name := filepath.Base(path)
		calls = append(calls, fmt.Sprintf("%s|%s|%v|%v", name, saveDir, selectFiles, sendToTelegram))
		if strings.HasPrefix(name, "bad") {
			return "", fmt.Errorf("corrupt metainfo")
		}
		return "hash-" + strings.TrimSuffix(name, ".torrent"), nil
	}

	h := &TorrentHandler{cfg: &config.Config{AppMode: "server"}, addTorrentFile: adder}
	router := gin.New()
	router.POST("/api/torrent/add", h.AddTorrent)

	postFiles := func(t *testing.T, selectFiles bool, sendToTelegram bool, filenames ...string) (int, map[string]any) {
		t.Helper()
		body := &bytes.Buffer{}
		writer := multipart.NewWriter(body)
		for _, name := range filenames {
			part, err := writer.CreateFormFile("file", name)
			if err != nil {
				t.Fatalf("create form file: %v", err)
			}
			_, _ = part.Write([]byte("d4:infod6:lengthi1ee"))
		}
		_ = writer.WriteField("save_directory", "./data/manager/downloads")
		_ = writer.WriteField("select_files", fmt.Sprintf("%v", selectFiles))
		_ = writer.WriteField("send_to_telegram", fmt.Sprintf("%v", sendToTelegram))
		if err := writer.Close(); err != nil {
			t.Fatalf("close multipart writer: %v", err)
		}

		req, err := http.NewRequest(http.MethodPost, "/api/torrent/add", body)
		if err != nil {
			t.Fatalf("new request: %v", err)
		}
		req.Header.Set("Content-Type", writer.FormDataContentType())

		w := httptest.NewRecorder()
		router.ServeHTTP(w, req)

		var parsed map[string]any
		_ = json.Unmarshal(w.Body.Bytes(), &parsed)
		return w.Code, parsed
	}

	tempEntries := func(t *testing.T) int {
		t.Helper()
		entries, err := os.ReadDir("./data/manager/temp")
		if err != nil {
			if os.IsNotExist(err) {
				return 0
			}
			t.Fatalf("read temp dir: %v", err)
		}
		return len(entries)
	}

	t.Run("single file keeps the legacy response shape", func(t *testing.T) {
		calls = nil
		code, resp := postFiles(t, false, false, "alpha.torrent")
		if code != http.StatusOK {
			t.Fatalf("status = %d, body = %v", code, resp)
		}
		if resp["status"] != "added" || resp["info_hash"] != "hash-alpha" {
			t.Fatalf("unexpected response: %v", resp)
		}
		if len(calls) != 1 || calls[0] != "alpha.torrent|./data/manager/downloads|false|false" {
			t.Fatalf("unexpected add calls: %v", calls)
		}
		if n := tempEntries(t); n != 0 {
			t.Fatalf("temp files not cleaned up: %d left", n)
		}
	})

	t.Run("batch adds every file one by one and continues past failures", func(t *testing.T) {
		calls = nil
		code, resp := postFiles(t, true, false, "one.torrent", "bad.torrent", "two.torrent")
		if code != http.StatusOK {
			t.Fatalf("status = %d, body = %v", code, resp)
		}
		results, ok := resp["results"].([]any)
		if !ok || len(results) != 3 {
			t.Fatalf("expected 3 per-file results, got: %v", resp)
		}
		first, _ := results[0].(map[string]any)
		if first["file"] != "one.torrent" || first["status"] != "added" || first["info_hash"] != "hash-one" {
			t.Fatalf("unexpected first result: %v", first)
		}
		second, _ := results[1].(map[string]any)
		if second["file"] != "bad.torrent" || second["status"] != "failed" || second["error"] == "" {
			t.Fatalf("unexpected second result: %v", second)
		}
		third, _ := results[2].(map[string]any)
		if third["file"] != "two.torrent" || third["status"] != "added" || third["info_hash"] != "hash-two" {
			t.Fatalf("unexpected third result: %v", third)
		}
		want := []string{
			"one.torrent|./data/manager/downloads|true|false",
			"bad.torrent|./data/manager/downloads|true|false",
			"two.torrent|./data/manager/downloads|true|false",
		}
		if strings.Join(calls, "\n") != strings.Join(want, "\n") {
			t.Fatalf("add calls = %v, want %v", calls, want)
		}
		if n := tempEntries(t); n != 0 {
			t.Fatalf("temp files not cleaned up: %d left", n)
		}
	})

	t.Run("failing single file surfaces the error", func(t *testing.T) {
		calls = nil
		code, resp := postFiles(t, false, false, "bad.torrent")
		if code != http.StatusInternalServerError {
			t.Fatalf("status = %d, body = %v", code, resp)
		}
		details, _ := resp["details"].(string)
		if !strings.Contains(details, "load torrent metadata") {
			t.Fatalf("expected failure phase in details: %v", resp)
		}
	})

	t.Run("send-to-telegram checkbox propagates to every add call", func(t *testing.T) {
		calls = nil
		code, resp := postFiles(t, false, true, "tg1.torrent", "tg2.torrent")
		if code != http.StatusOK {
			t.Fatalf("status = %d, body = %v", code, resp)
		}
		want := []string{
			"tg1.torrent|./data/manager/downloads|false|true",
			"tg2.torrent|./data/manager/downloads|false|true",
		}
		if strings.Join(calls, "\n") != strings.Join(want, "\n") {
			t.Fatalf("add calls = %v, want %v", calls, want)
		}
	})

	t.Run("no payload is rejected", func(t *testing.T) {
		req := httptest.NewRequest(http.MethodPost, "/api/torrent/add", strings.NewReader("{}"))
		req.Header.Set("Content-Type", "application/json")
		w := httptest.NewRecorder()
		router.ServeHTTP(w, req)
		if w.Code != http.StatusBadRequest {
			t.Fatalf("status = %d, want 400", w.Code)
		}
	})
}
