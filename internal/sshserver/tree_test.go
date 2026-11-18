package sshserver

import (
	"bufio"
	"fmt"
	"io"
	"os"
	"path"
	"sort"
	"strconv"
	"strings"
	"testing"

	"github.com/UnixEU/ssh-llm-tui/internal/uploads"
	"github.com/pkg/sftp"
	gossh "golang.org/x/crypto/ssh"
)

func TestSFTPNestedTree(t *testing.T) {
	store := uploads.New(t.TempDir(), 1<<20, 100)
	addr := startUploadServer(t, store)
	client, err := sftp.NewClient(dialUser(t, addr))
	if err != nil {
		t.Fatal(err)
	}
	defer client.Close()

	put := func(remote, body string) {
		t.Helper()
		f, err := client.Create(remote)
		if err != nil {
			t.Fatalf("create %s: %v", remote, err)
		}
		if _, err := io.WriteString(f, body); err != nil {
			t.Fatal(err)
		}
		if err := f.Close(); err != nil {
			t.Fatalf("close %s: %v", remote, err)
		}
	}
	// What OpenSSH scp -r does in SFTP mode: mkdir each level, then put.
	for _, d := range []string{"/uploads/proj", "/uploads/proj/src", "/uploads/proj/src/util", "/uploads/proj/docs"} {
		if err := client.Mkdir(d); err != nil {
			t.Fatalf("mkdir %s: %v", d, err)
		}
	}
	put("/uploads/proj/README.md", "readme")
	put("/uploads/proj/src/main.go", "package main")
	put("/uploads/proj/src/util/str.go", "package util")
	put("uploads/proj/docs/guide.md", "guide")
	// Parents are created when a client skips mkdir.
	put("other/deep/file.txt", "deep")
	// Ignored components are accepted and dropped.
	if err := client.Mkdir("/uploads/proj/.git"); err != nil {
		t.Fatal(err)
	}
	if err := client.MkdirAll("/uploads/proj/.git/objects/ab"); err != nil {
		t.Fatal(err)
	}
	put("/uploads/proj/.git/objects/ab/cdef", "blob")
	put("/uploads/proj/.git/HEAD", "ref: refs/heads/main")

	if body, err := store.Read("user", "proj/src/util/str.go"); err != nil || string(body) != "package util" {
		t.Fatalf("store %q %v", body, err)
	}
	disk, _ := store.FilePath("user", "proj/docs/guide.md")
	if _, err := os.Stat(disk); err != nil {
		t.Fatal(err)
	}
	if files, _ := store.Usage("user"); files != 5 {
		t.Fatalf("files %d", files)
	}

	names := func(dir string) string {
		t.Helper()
		entries, err := client.ReadDir(dir)
		if err != nil {
			t.Fatalf("readdir %s: %v", dir, err)
		}
		var out []string
		for _, e := range entries {
			n := e.Name()
			if e.IsDir() {
				n += "/"
			}
			out = append(out, n)
		}
		sort.Strings(out)
		return strings.Join(out, " ")
	}
	if got := names("/uploads"); got != "other/ proj/" {
		t.Fatalf("root: %s", got)
	}
	if got := names("/uploads/proj"); got != "README.md docs/ src/" {
		t.Fatalf("proj: %s", got)
	}
	if got := names("uploads/proj/src"); got != "main.go util/" {
		t.Fatalf("src: %s", got)
	}
	if got := names("/uploads/proj/.git"); got != "" {
		t.Fatalf(".git: %q", got)
	}

	st, err := client.Stat("/uploads/proj/src")
	if err != nil || !st.IsDir() || st.Name() != "src" {
		t.Fatalf("stat dir %+v %v", st, err)
	}
	st, err = client.Lstat("/uploads/proj/src/main.go")
	if err != nil || st.IsDir() || st.Size() != int64(len("package main")) {
		t.Fatalf("lstat file %+v %v", st, err)
	}
	if _, err := client.Stat("/uploads/proj/missing"); err == nil {
		t.Fatal("stat missing")
	}
	if rp, err := client.RealPath("proj/src"); err != nil || rp != "/uploads/proj/src" {
		t.Fatalf("realpath %q %v", rp, err)
	}
	if rp, err := client.RealPath("."); err != nil || rp != "/uploads" {
		t.Fatalf("realpath . %q %v", rp, err)
	}

	// Download a nested file.
	f, err := client.Open("/uploads/proj/src/main.go")
	if err != nil {
		t.Fatal(err)
	}
	body, _ := io.ReadAll(f)
	f.Close()
	if string(body) != "package main" {
		t.Fatalf("get %q", body)
	}

	if err := client.Rename("/uploads/proj/docs/guide.md", "/uploads/proj/guide.md"); err != nil {
		t.Fatal(err)
	}
	if _, err := store.Read("user", "proj/guide.md"); err != nil {
		t.Fatal(err)
	}
	if err := client.Remove("/uploads/proj/README.md"); err != nil {
		t.Fatal(err)
	}
	if err := client.RemoveDirectory("/uploads/proj/docs"); err != nil {
		t.Fatalf("rmdir empty: %v", err)
	}
	if err := client.RemoveDirectory("/uploads/proj/src"); err != nil {
		t.Fatalf("rmdir tree: %v", err)
	}
	if got := names("/uploads/proj"); got != "guide.md" {
		t.Fatalf("after removes: %s", got)
	}
	if err := client.RemoveDirectory("/uploads"); err == nil {
		t.Fatal("removed the root")
	}
	if _, err := client.Create("/uploads/proj/guide.md/inner.txt"); err == nil {
		t.Fatal("file under a file")
	}
}

func TestSFTPLimitFailsOneFile(t *testing.T) {
	store := uploads.New(t.TempDir(), 1<<20, 2)
	addr := startUploadServer(t, store)
	client, err := sftp.NewClient(dialUser(t, addr))
	if err != nil {
		t.Fatal(err)
	}
	defer client.Close()
	for i, want := range []bool{true, true, false} {
		f, err := client.Create(fmt.Sprintf("/uploads/d/f%d.txt", i))
		ok := err == nil
		if ok {
			_, _ = io.WriteString(f, "x")
			ok = f.Close() == nil
		}
		if ok != want {
			t.Fatalf("file %d ok=%v want %v (%v)", i, ok, want, err)
		}
	}
	if files, _ := store.Usage("user"); files != 2 {
		t.Fatal(files)
	}
}

// scpRecursive speaks the legacy scp sink protocol for a small tree.
func scpRecursive(conn *gossh.Client, target string, lines []string, bodies map[string]string) error {
	session, err := conn.NewSession()
	if err != nil {
		return err
	}
	defer session.Close()
	stdin, _ := session.StdinPipe()
	stdout, _ := session.StdoutPipe()
	if err := session.Start("scp -r -t " + target); err != nil {
		return err
	}
	ack := make([]byte, 1)
	readAck := func() error {
		if _, err := io.ReadFull(stdout, ack); err != nil {
			return err
		}
		if ack[0] != 0 {
			msg, _ := bufio.NewReader(stdout).ReadString('\n')
			return fmt.Errorf("scp error: %s", msg)
		}
		return nil
	}
	if err := readAck(); err != nil {
		return err
	}
	for _, line := range lines {
		if strings.HasPrefix(line, "C") {
			name := line[strings.LastIndex(line, " ")+1:]
			body := bodies[name]
			line = "C0644 " + strconv.Itoa(len(body)) + " " + name
			if _, err := io.WriteString(stdin, line+"\n"); err != nil {
				return err
			}
			if err := readAck(); err != nil {
				return err
			}
			if _, err := io.WriteString(stdin, body+"\x00"); err != nil {
				return err
			}
			if err := readAck(); err != nil {
				return err
			}
			continue
		}
		if _, err := io.WriteString(stdin, line+"\n"); err != nil {
			return err
		}
		if err := readAck(); err != nil {
			return err
		}
	}
	_ = stdin.Close()
	return session.Wait()
}

func TestLegacySCPRecursiveUpload(t *testing.T) {
	bodies := map[string]string{"README.md": "readme", "main.go": "package main", "str.go": "package util", "HEAD": "ref", "a.txt": "a", "b.txt": "b"}
	lines := []string{
		"D0755 0 proj",
		"C README.md",
		"D0755 0 .git",
		"C HEAD",
		"E",
		"D0755 0 src",
		"C main.go",
		"D0755 0 util",
		"C str.go",
		"E",
		"E",
		"D0755 0 ab",
		"C a.txt",
		"E",
		"E",
		"C b.txt",
	}
	for _, target := range []string{"/uploads", "uploads/", ".", "uploads/nested/into", "uploads/exists"} {
		t.Run(target, func(t *testing.T) {
			store := uploads.New(t.TempDir(), 1<<20, 100)
			if err := store.Mkdir("user", "exists"); err != nil {
				t.Fatal(err)
			}
			addr := startUploadServer(t, store)
			conn := dialUser(t, addr)
			if err := scpRecursive(conn, target, lines, bodies); err != nil {
				t.Fatal(err)
			}
			// A target that does not exist yet becomes the top directory,
			// as with a regular scp server.
			proj, top := "proj/", ""
			if target == "uploads/nested/into" {
				proj, top = "nested/into/", "nested/into/"
			}
			if target == "uploads/exists" {
				proj, top = "exists/proj/", "exists/"
			}
			want := []string{top + "b.txt", proj + "README.md", proj + "ab/a.txt", proj + "src/main.go", proj + "src/util/str.go"}
			var got []string
			for _, f := range store.List("user") {
				got = append(got, f.Name)
			}
			sort.Strings(want)
			if strings.Join(got, ",") != strings.Join(want, ",") {
				t.Fatalf("got %v\nwant %v", got, want)
			}
			body, _ := store.Read("user", proj+"src/util/str.go")
			if string(body) != "package util" {
				t.Fatalf("body %q", body)
			}
			disk, _ := store.FilePath("user", proj+"ab/a.txt")
			if _, err := os.Stat(disk); err != nil {
				t.Fatal(err)
			}
			if _, err := os.Stat(path.Join(store.Dir(), "user", proj+".git")); !os.IsNotExist(err) {
				t.Fatal(".git stored")
			}
		})
	}
}

func TestLegacySCPRecursiveDownload(t *testing.T) {
	store := uploads.New(t.TempDir(), 1<<20, 100)
	for _, p := range []string{"a/x.txt", "a/b/y.txt", "ab/z.txt", "top.txt"} {
		if err := store.Put("user", p, []byte(p)); err != nil {
			t.Fatal(err)
		}
	}
	addr := startUploadServer(t, store)
	conn := dialUser(t, addr)
	got := scpSource(t, conn, "scp -r -f /uploads/a")
	if !strings.HasPrefix(got[0], "D") || !strings.HasSuffix(got[0], " a") {
		t.Fatalf("tree %v", got)
	}
	joined := strings.Join(got, "|")
	if !strings.Contains(joined, "D0755 0 b|C0644 9 y.txt|E") || !strings.Contains(joined, "C0644 7 x.txt") || strings.Contains(joined, "z.txt") {
		t.Fatalf("tree %s", joined)
	}
	root := strings.Join(scpSource(t, conn, "scp -r -f uploads/"), "|")
	for _, part := range []string{"D0755 0 ab|C0644 8 z.txt|E", "D0755 0 a|", "C0644 7 top.txt"} {
		if !strings.Contains(root, part) {
			t.Fatalf("root %s missing %s", root, part)
		}
	}
	single := strings.Join(scpSource(t, conn, "scp -f uploads/a/b/y.txt"), "|")
	if single != "C0644 9 y.txt" {
		t.Fatalf("single %s", single)
	}
}

// scpSource runs scp -f and returns the D, C and E lines it sends.
func scpSource(t *testing.T, conn *gossh.Client, cmd string) []string {
	t.Helper()
	session, err := conn.NewSession()
	if err != nil {
		t.Fatal(err)
	}
	defer session.Close()
	stdin, _ := session.StdinPipe()
	stdout, _ := session.StdoutPipe()
	if err := session.Start(cmd); err != nil {
		t.Fatal(err)
	}
	r := bufio.NewReader(stdout)
	var lines []string
	_, _ = stdin.Write([]byte{0})
	for {
		line, err := r.ReadString('\n')
		if err != nil {
			break
		}
		line = strings.TrimSuffix(line, "\n")
		switch {
		case strings.HasPrefix(line, "T"):
		case strings.HasPrefix(line, "C"):
			lines = append(lines, line)
			fields := strings.Fields(line)
			n, _ := strconv.Atoi(fields[1])
			buf := make([]byte, n+1)
			if _, err := io.ReadFull(r, buf); err != nil {
				t.Fatal(err)
			}
		default:
			lines = append(lines, line)
		}
		_, _ = stdin.Write([]byte{0})
	}
	_ = stdin.Close()
	_ = session.Wait()
	return lines
}
