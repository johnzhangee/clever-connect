package s3store

import (
	"strings"
	"testing"
)

const sampleS3Cfg = `[default]
access_key = BKIKJAA5BMMU2TOKU7VJ
secret_key = 9f2a7c1d000bc0pl3n8sxl0x3m9mb07pl0x3m9mbsl0x3m9mb0
host_base = cellar.services.clever-cloud.com
use_https = True
host_bucket = %(bucket)s.cellar.services.clever-cloud.com
bucket_location = eu-west-3
`

func TestParseS3CfgCookie(t *testing.T) {
	values := parseS3CfgCookie(strings.NewReader(sampleS3Cfg))
	if got := values["access_key"]; got != "BKIKJAA5BMMU2TOKU7VJ" {
		t.Errorf("access_key = %q", got)
	}
	if got := values["secret_key"]; got != "9f2a7c1d000bc0pl3n8sxl0x3m9mb07pl0x3m9mbsl0x3m9mb0" {
		t.Errorf("secret_key = %q", got)
	}
	if got := values["use_https"]; got != "True" {
		t.Errorf("use_https = %q", got)
	}
	if _, ok := values["[default"]; ok {
		t.Error("section headers must be skipped")
	}
}

func TestBuildConfigFromS3CfgValues(t *testing.T) {
	cfg := BuildConfigFromS3CfgValues(map[string]string{
		"host_base":       "cellar.services.clever-cloud.com",
		"access_key":      "AK",
		"secret_key":      "SK",
		"bucket_location": "eu-west-3",
		"use_https":       "True",
	}, "cellar-cellar_9a75d5a4-x9y2-s3cfg")

	if cfg.Endpoint != "cellar.services.clever-cloud.com" {
		t.Errorf("Endpoint = %q", cfg.Endpoint)
	}
	if !cfg.Secure {
		t.Error("use_https=True must yield Secure")
	}
	if cfg.Region != "eu-west-3" {
		t.Errorf("Region = %q", cfg.Region)
	}
	if cfg.Bucket != "cellar_9a75d5a4-x9y2" {
		t.Errorf("Bucket = %q", cfg.Bucket)
	}
}

func TestDeriveBucketFromConfigFilename(t *testing.T) {
	cases := map[string]string{
		"cellar-cellar_9a75d5a4-07ea-44e9-b3cd-19c7c9a94b81-s3cfg": "cellar_9a75d5a4-07ea-44e9-b3cd-19c7c9a94b81",
		"plain.s3cfg":       "plain",
		"custom-name.s3cfg": "custom-name",
	}
	for in, want := range cases {
		if got := DeriveBucketFromConfigFilename(in); got != want {
			t.Errorf("DeriveBucketFromConfigFilename(%q) = %q; want %q", in, got, want)
		}
	}
}

func TestKeyForTorrentFile(t *testing.T) {
	// traversal sanitization: the ".." segment must be dropped while the rest
	// of the path stays inside the torrent subtree
	got := KeyForTorrentFile("clever-connect", "ABC123", "../Season 1/Ep 01.mp4")
	want := "clever-connect/torrents/ABC123/Season 1/Ep 01.mp4"
	if got != want {
		t.Errorf("KeyForTorrentFile = %q; want %q", got, want)
	}
	// empty prefix support
	got = KeyForTorrentFile("", "H", "movie.mkv")
	if got != "torrents/H/movie.mkv" {
		t.Errorf("KeyForTorrentFile (no prefix) = %q", got)
	}
}

func TestTorrentPrefix(t *testing.T) {
	if got := TorrentPrefix("clever-connect/", "H1"); got != "clever-connect/torrents/H1/" {
		t.Errorf("TorrentPrefix = %q", got)
	}
	if got := TorrentPrefix("", "H1"); got != "torrents/H1/" {
		t.Errorf("TorrentPrefix (no prefix) = %q", got)
	}
}
