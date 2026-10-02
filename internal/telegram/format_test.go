package telegram

import (
	"strings"
	"testing"
)

// balancedTags reports whether every opening HTML tag has a matching closing tag.
func balancedTags(s, tag string) bool {
	return strings.Count(s, "<"+tag+">") == strings.Count(s, "</"+tag+">")
}

func TestFormatUploadProgressHTML(t *testing.T) {
	got := formatUploadProgressHTML("17 - My Pink Heaven.mp4", 494*1024*1024, 1027*1024*1024, 48, 11.4, 44)

	for _, want := range []string{
		"<b>UPLOADING FILE</b>",
		"<code>17 - My Pink Heaven.mp4</code>",
		"<b>48%</b>",
		"<code>11.40 MB/s</code>",
		"🔷 <i>CleverConnect Engine</i>",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("progress message missing %q:\n%s", want, got)
		}
	}
	for _, tag := range []string{"b", "code", "i"} {
		if !balancedTags(got, tag) {
			t.Errorf("progress message has unbalanced <%s> tags:\n%s", tag, got)
		}
	}
}

func TestFormatFunctionsEscapeUserContent(t *testing.T) {
	// File names and paths containing HTML-significant characters must be
	// escaped so Telegram HTML parsing cannot break (nor inject formatting).
	name := `a<b&c>d.mp4`

	progress := formatUploadProgressHTML(name, 1, 2, 50, 1, 1)
	initial := formatUploadInitialHTML(name, 2)
	success := formatSuccessHTML(true, name, 2, "/downloads/"+name)
	errMsg := formatErrorHTML(true, name, "boom <&> fail")

	for _, tc := range []struct{ label, got string }{
		{"progress", progress},
		{"initial", initial},
		{"success", success},
		{"error", errMsg},
	} {
		if !strings.Contains(tc.got, "a&lt;b&amp;c&gt;d.mp4") {
			t.Errorf("%s: file name not HTML-escaped:\n%s", tc.label, tc.got)
		}
		if strings.Contains(tc.got, "<b&") || strings.Contains(tc.got, "<code>&") {
			t.Errorf("%s: unescaped raw content leaked into markup:\n%s", tc.label, tc.got)
		}
		for _, tag := range []string{"b", "code", "i"} {
			if !balancedTags(tc.got, tag) {
				t.Errorf("%s: unbalanced <%s> tags:\n%s", tc.label, tag, tc.got)
			}
		}
	}
}

func TestFormatMessagesAreHTMLNotMarkdown(t *testing.T) {
	// The MTProto path renders these strings as Telegram HTML. Any leftover
	// markdown asterisks/backticks would show up literally in the client.
	messages := []string{
		formatUploadProgressHTML("f.mp4", 1, 2, 50, 1, 1),
		formatDownloadProgressHTML("f.mp4", 1, 2, 50, 1, 1),
		formatUploadInitialHTML("f.mp4", 2),
		formatDownloadInitialHTML("f.mp4", 2),
		formatSuccessHTML(true, "f.mp4", 2, "/downloads/f.mp4"),
		formatSuccessHTML(false, "f.mp4", 2, "/downloads/f.mp4"),
		formatErrorHTML(true, "f.mp4", "reason"),
		formatErrorHTML(false, "f.mp4", "reason"),
	}
	for i, m := range messages {
		if strings.Contains(m, "*") || strings.Contains(m, "`") {
			t.Errorf("message %d still contains markdown markers:\n%s", i, m)
		}
	}
}

func TestMakeProgressBar(t *testing.T) {
	empty := makeProgressBar(0, 15)
	if !strings.Contains(empty, "🟠") || !strings.Contains(empty, "▐") || !strings.Contains(empty, "▌") {
		t.Errorf("0%% bar malformed: %s", empty)
	}
	if n := strings.Count(empty, "░"); n != 15 {
		t.Errorf("0%% bar should have 15 empty cells, got %d: %s", n, empty)
	}
	if strings.Contains(empty, "`") || strings.Contains(empty, "%") {
		t.Errorf("bar must not embed markdown/percent itself anymore: %s", empty)
	}

	full := makeProgressBar(100, 15)
	if n := strings.Count(full, "█"); n != 15 {
		t.Errorf("100%% bar should have 15 filled cells, got %d: %s", n, full)
	}
	if !strings.Contains(full, "✅") {
		t.Errorf("100%% bar should use the done indicator: %s", full)
	}

	half := makeProgressBar(50, 15)
	if strings.Count(half, "█") != 7 || strings.Count(half, "░") != 8 {
		t.Errorf("50%% bar cell split wrong: %s", half)
	}
	if !strings.Contains(half, "🔵") {
		t.Errorf("50%% bar should use the blue indicator: %s", half)
	}

	if out := makeProgressBar(-5, 15); out != empty {
		t.Errorf("negative percent should clamp to 0%%: %s", out)
	}
	if out := makeProgressBar(150, 15); out != full {
		t.Errorf("over-100 percent should clamp to 100%%: %s", out)
	}
}

func TestEscapeHTML(t *testing.T) {
	if got := escapeHTML(`a&b<c>"d"`); got != `a&amp;b&lt;c&gt;"d"` {
		t.Errorf("escapeHTML = %q", got)
	}
	if got := escapeHTML("plain"); got != "plain" {
		t.Errorf("escapeHTML should not alter plain text: %q", got)
	}
}
