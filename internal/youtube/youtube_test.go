package youtube

import "testing"

func TestParseVTTDeduplicatesRollingCaptions(t *testing.T) {
	vtt := `WEBVTT
Kind: captions
Language: en

00:00:00.000 --> 00:00:02.000
<c>Hello</c> <00:00:00.500><c>world</c>

00:00:02.000 --> 00:00:04.000
Hello world
this is a test

00:00:04.000 --> 00:00:06.000
this is a test
`
	segs := ParseVTT(vtt)
	if len(segs) != 2 {
		t.Fatalf("expected 2 segments, got %d: %+v", len(segs), segs)
	}
	if segs[0].Text != "Hello world" {
		t.Errorf("unexpected seg[0].Text: %q", segs[0].Text)
	}
	if segs[1].Text != "this is a test" {
		t.Errorf("unexpected seg[1].Text: %q", segs[1].Text)
	}
}

func TestPickVTTURLPrefersLanguageAndOrig(t *testing.T) {
	tracks := map[string][]SubtitleTrack{
		"de":      {{Ext: "vtt", URL: "https://example.com/de.vtt"}},
		"ru-orig": {{Ext: "vtt", URL: "https://example.com/ru-orig.vtt"}},
		"en":      {{Ext: "json3", URL: "https://example.com/en.json3"}, {Ext: "vtt", URL: "https://example.com/en.vtt"}},
	}
	if got := pickVTTURL(tracks, []string{"ru", "en"}); got != "https://example.com/ru-orig.vtt" {
		t.Errorf("expected ru-orig.vtt, got %q", got)
	}
	if got := pickVTTURL(tracks, []string{"fr"}); got != "https://example.com/ru-orig.vtt" {
		t.Errorf("expected fallback to *-orig, got %q", got)
	}
}
