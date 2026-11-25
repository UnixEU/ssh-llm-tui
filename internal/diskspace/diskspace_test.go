package diskspace

import "testing"

func TestReadTempDir(t *testing.T) {
	info, err := Read(t.TempDir())
	if err != nil {
		t.Skip(err)
	}
	if info.TotalBytes <= 0 || info.FreeBytes < 0 || info.FreeBytes > info.TotalBytes {
		t.Fatalf("%+v", info)
	}
}

func TestShort(t *testing.T) {
	if !Short(Info{TotalBytes: 100}, 200) {
		t.Fatal("100 < 200 should be short")
	}
	if Short(Info{TotalBytes: 300}, 200) || Short(Info{}, 200) {
		t.Fatal("not short")
	}
}
