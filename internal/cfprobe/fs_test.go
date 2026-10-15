package cfprobe

import (
	"encoding/base64"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestHandleFSListRejectsTraversal(t *testing.T) {
	reply := handleFSPacket(fsPacket{Type: "fs_list", ID: "1", Path: "/tmp/../etc/passwd"})
	if reply.Error == "" && reply.Type != "fs_error" {
		if !strings.HasSuffix(filepath.Clean(reply.Path), "passwd") && len(reply.Entries) > 0 {
			t.Fatalf("traversal listed unexpected entries: %+v", reply)
		}
	}
	if reply.Type == "fs_list_result" && reply.Path == "/etc/passwd" {
		t.Fatal("fs_list must not treat a file as a directory via traversal")
	}
}

func TestHandleFSListDirectory(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "hello.txt"), []byte("hi"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(filepath.Join(dir, "sub"), 0o755); err != nil {
		t.Fatal(err)
	}
	reply := handleFSPacket(fsPacket{Type: "fs_list", ID: "list1", Path: dir})
	if reply.Type != "fs_list_result" {
		t.Fatalf("type=%q error=%q", reply.Type, reply.Error)
	}
	if reply.ID != "list1" {
		t.Fatalf("id=%q", reply.ID)
	}
	names := map[string]fsEntry{}
	for _, entry := range reply.Entries {
		names[entry.Name] = entry
	}
	if _, ok := names["hello.txt"]; !ok {
		t.Fatalf("missing hello.txt in %v", reply.Entries)
	}
	if !names["sub"].IsDir {
		t.Fatal("sub should be a directory")
	}
}

func TestHandleFSReadAndWrite(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "note.txt")
	payload := base64.StdEncoding.EncodeToString([]byte("abc"))
	write := handleFSPacket(fsPacket{
		Type:   "fs_write",
		ID:     "w1",
		Path:   path,
		Offset: 0,
		Data:   payload,
		EOF:    true,
	})
	if write.Type != "fs_ok" {
		t.Fatalf("write type=%q error=%q", write.Type, write.Error)
	}
	read := handleFSPacket(fsPacket{Type: "fs_read", ID: "r1", Path: path, Offset: 0, Length: 16})
	if read.Type != "fs_data" {
		t.Fatalf("read type=%q error=%q", read.Type, read.Error)
	}
	got, err := base64.StdEncoding.DecodeString(read.Data)
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != "abc" {
		t.Fatalf("data=%q", got)
	}
	if !read.EOF {
		t.Fatal("expected eof")
	}
}

func TestHandleFSRejectsEmptyPath(t *testing.T) {
	reply := handleFSPacket(fsPacket{Type: "fs_list", ID: "x", Path: ""})
	if reply.Type == "fs_error" {
		return
	}
	if reply.Path != "/" && !strings.HasPrefix(reply.Path, string(os.PathSeparator)) {
		t.Fatalf("empty path should default to absolute root, got %+v", reply)
	}
}

func TestHandleFSMkdirAndRemove(t *testing.T) {
	dir := t.TempDir()
	target := filepath.Join(dir, "newdir")
	mkdir := handleFSPacket(fsPacket{Type: "fs_mkdir", ID: "m1", Path: target})
	if mkdir.Type != "fs_ok" {
		t.Fatalf("mkdir type=%q error=%q", mkdir.Type, mkdir.Error)
	}
	info, err := os.Stat(target)
	if err != nil || !info.IsDir() {
		t.Fatalf("mkdir did not create directory: %v", err)
	}
	rm := handleFSPacket(fsPacket{Type: "fs_rm", ID: "d1", Path: target})
	if rm.Type != "fs_ok" {
		t.Fatalf("rm type=%q error=%q", rm.Type, rm.Error)
	}
	if _, err := os.Stat(target); !os.IsNotExist(err) {
		t.Fatal("directory still exists")
	}
}
