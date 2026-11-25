package config

import "testing"

func TestParseBytes(t *testing.T) {
	for in, want := range map[string]int64{
		"10485760": 10 << 20,
		"512k":     512 << 10,
		"256M":     256 << 20,
		"256m":     256 << 20,
		"256MB":    256 << 20,
		"256MiB":   256 << 20,
		"256 MiB":  256 << 20,
		"1G":       1 << 30,
		"1gib":     1 << 30,
		"1.5G":     3 << 29,
		"2T":       2 << 40,
		"100B":     100,
	} {
		got, err := ParseBytes(in)
		if err != nil || got != want {
			t.Errorf("%q = %d, %v; want %d", in, got, err, want)
		}
	}
	for _, in := range []string{"", "M", "abc", "10X", "1..2G", "-1"} {
		if _, err := ParseBytes(in); err == nil {
			t.Errorf("%q accepted", in)
		}
	}
}

func TestFormatBytes(t *testing.T) {
	for n, want := range map[int64]string{100: "100 B", 256 << 20: "256 MiB", 3 << 29: "1.5 GiB"} {
		if got := FormatBytes(n); got != want {
			t.Errorf("%d = %q, want %q", n, got, want)
		}
	}
}

func TestSizeEnv(t *testing.T) {
	cfg, err := Load(LoadOptions{Getenv: envFrom(map[string]string{
		"UPLOAD_MAX_BYTES":         "20M",
		"UPLOAD_MAX_TOTAL_BYTES":   "1G",
		"DOWNLOAD_MAX_TOTAL_BYTES": "512MiB",
		"IMAGE_MAX_BYTES":          "4MB",
	})})
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Uploads.MaxBytes != 20<<20 || cfg.Uploads.MaxTotalBytes != 1<<30 || cfg.Downloads.MaxTotalBytes != 512<<20 || cfg.Uploads.ImageMaxBytes != 4<<20 {
		t.Fatalf("%+v %+v", cfg.Uploads, cfg.Downloads)
	}
	for _, env := range []map[string]string{
		{"UPLOAD_MAX_TOTAL_BYTES": "0M"},
		{"UPLOAD_MAX_BYTES": "ten"},
		{"DOWNLOAD_MAX_TOTAL_BYTES": "5Q"},
	} {
		if _, err := Load(LoadOptions{Getenv: envFrom(env)}); err == nil {
			t.Errorf("accepted %v", env)
		}
	}
}
