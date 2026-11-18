package transfer

import "testing"

func TestResolve(t *testing.T) {
	cases := []struct {
		in   string
		area area
		rel  string
	}{
		{"", areaRoot, ""},
		{"/", areaRoot, ""},
		{".", areaRoot, ""},
		{"~", areaRoot, ""},
		{"uploads", areaUploads, ""},
		{"/uploads/", areaUploads, ""},
		{"/uploads/proj/a.go", areaUploads, "proj/a.go"},
		{"proj/a.go", areaUploads, "proj/a.go"},
		{"/notes.txt", areaUploads, "notes.txt"},
		{"~/uploads/x", areaUploads, "x"},
		{"/uploads/downloads/x", areaUploads, "downloads/x"},
		{"downloads", areaDownloads, ""},
		{"/downloads/", areaDownloads, ""},
		{"~/downloads/files/proj/a.go", areaDownloads, "files/proj/a.go"},
		{"downloads/changes.patch", areaDownloads, "changes.patch"},
	}
	for _, c := range cases {
		a, rel, err := resolve(c.in)
		if err != nil || a != c.area || rel != c.rel {
			t.Fatalf("%q -> %v %q %v, want %v %q", c.in, a, rel, err, c.area, c.rel)
		}
	}
	for _, bad := range []string{"../x", "/downloads/../uploads", "downloads/a/../../x"} {
		if _, _, err := resolve(bad); err == nil {
			t.Fatalf("%q accepted", bad)
		}
	}
	if vpath(areaDownloads, "files/a") != "downloads/files/a" || vpath(areaUploads, "") != "uploads" || vpath(areaRoot, "") != "" {
		t.Fatal("vpath")
	}
}
