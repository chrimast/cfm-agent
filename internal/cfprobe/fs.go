package cfprobe

import (
	"encoding/base64"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

const fsChunkSize = 24 * 1024

type fsPacket struct {
	Type    string    `json:"type"`
	ID      string    `json:"id,omitempty"`
	Path    string    `json:"path,omitempty"`
	NewPath string    `json:"new_path,omitempty"`
	Offset  int64     `json:"offset,omitempty"`
	Length  int64     `json:"length,omitempty"`
	Data    string    `json:"data,omitempty"`
	EOF     bool      `json:"eof,omitempty"`
	Error   string    `json:"error,omitempty"`
	Entries []fsEntry `json:"entries,omitempty"`
	Size    int64     `json:"size,omitempty"`
}

type fsEntry struct {
	Name  string `json:"name"`
	Path  string `json:"path"`
	IsDir bool   `json:"is_dir"`
	Size  int64  `json:"size"`
	MTime int64  `json:"mtime,omitempty"`
}

func fsError(id, message string) fsPacket {
	return fsPacket{Type: "fs_error", ID: id, Error: message}
}

func resolveFSPath(raw string) (string, error) {
	path := strings.TrimSpace(raw)
	if path == "" {
		path = "/"
	}
	if !filepath.IsAbs(path) {
		return "", fmt.Errorf("path must be absolute")
	}
	cleaned := filepath.Clean(path)
	if cleaned == "" {
		cleaned = "/"
	}
	return cleaned, nil
}

func handleFSPacket(msg fsPacket) fsPacket {
	switch msg.Type {
	case "fs_list":
		return fsList(msg)
	case "fs_read":
		return fsRead(msg)
	case "fs_write":
		return fsWrite(msg)
	case "fs_mkdir":
		return fsMkdir(msg)
	case "fs_rm":
		return fsRemove(msg)
	case "fs_rename":
		return fsRename(msg)
	default:
		return fsError(msg.ID, "unknown fs command")
	}
}

func fsList(msg fsPacket) fsPacket {
	path, err := resolveFSPath(msg.Path)
	if err != nil {
		return fsError(msg.ID, err.Error())
	}
	info, err := os.Stat(path)
	if err != nil {
		return fsError(msg.ID, err.Error())
	}
	if !info.IsDir() {
		return fsError(msg.ID, "not a directory")
	}
	entries, err := os.ReadDir(path)
	if err != nil {
		return fsError(msg.ID, err.Error())
	}
	out := make([]fsEntry, 0, len(entries))
	for _, entry := range entries {
		item := fsEntry{
			Name:  entry.Name(),
			Path:  filepath.Join(path, entry.Name()),
			IsDir: entry.IsDir(),
		}
		if info, err := entry.Info(); err == nil {
			item.Size = info.Size()
			item.MTime = info.ModTime().Unix()
			item.IsDir = info.IsDir()
		}
		out = append(out, item)
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].IsDir != out[j].IsDir {
			return out[i].IsDir
		}
		return strings.ToLower(out[i].Name) < strings.ToLower(out[j].Name)
	})
	return fsPacket{Type: "fs_list_result", ID: msg.ID, Path: path, Entries: out}
}

func fsRead(msg fsPacket) fsPacket {
	path, err := resolveFSPath(msg.Path)
	if err != nil {
		return fsError(msg.ID, err.Error())
	}
	file, err := os.Open(path)
	if err != nil {
		return fsError(msg.ID, err.Error())
	}
	defer file.Close()
	info, err := file.Stat()
	if err != nil {
		return fsError(msg.ID, err.Error())
	}
	if info.IsDir() {
		return fsError(msg.ID, "not a file")
	}
	length := msg.Length
	if length <= 0 || length > fsChunkSize {
		length = fsChunkSize
	}
	if _, err := file.Seek(msg.Offset, io.SeekStart); err != nil {
		return fsError(msg.ID, err.Error())
	}
	buf := make([]byte, length)
	n, err := io.ReadFull(file, buf)
	eof := false
	if err == io.EOF || err == io.ErrUnexpectedEOF {
		eof = true
		buf = buf[:n]
		err = nil
	}
	if err != nil {
		return fsError(msg.ID, err.Error())
	}
	if int64(n) < length || msg.Offset+int64(n) >= info.Size() {
		eof = true
	}
	return fsPacket{
		Type:   "fs_data",
		ID:     msg.ID,
		Path:   path,
		Offset: msg.Offset,
		Data:   base64.StdEncoding.EncodeToString(buf[:n]),
		EOF:    eof,
		Size:   info.Size(),
	}
}

func fsWrite(msg fsPacket) fsPacket {
	path, err := resolveFSPath(msg.Path)
	if err != nil {
		return fsError(msg.ID, err.Error())
	}
	raw, err := base64.StdEncoding.DecodeString(msg.Data)
	if err != nil {
		return fsError(msg.ID, "invalid base64")
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return fsError(msg.ID, err.Error())
	}
	flag := os.O_WRONLY | os.O_CREATE
	if msg.Offset == 0 {
		flag |= os.O_TRUNC
	}
	file, err := os.OpenFile(path, flag, 0o644)
	if err != nil {
		return fsError(msg.ID, err.Error())
	}
	defer file.Close()
	if _, err := file.Seek(msg.Offset, io.SeekStart); err != nil {
		return fsError(msg.ID, err.Error())
	}
	if _, err := file.Write(raw); err != nil {
		return fsError(msg.ID, err.Error())
	}
	return fsPacket{Type: "fs_ok", ID: msg.ID, Path: path, Offset: msg.Offset, EOF: msg.EOF}
}

func fsMkdir(msg fsPacket) fsPacket {
	path, err := resolveFSPath(msg.Path)
	if err != nil {
		return fsError(msg.ID, err.Error())
	}
	if err := os.MkdirAll(path, 0o755); err != nil {
		return fsError(msg.ID, err.Error())
	}
	return fsPacket{Type: "fs_ok", ID: msg.ID, Path: path}
}

func fsRemove(msg fsPacket) fsPacket {
	path, err := resolveFSPath(msg.Path)
	if err != nil {
		return fsError(msg.ID, err.Error())
	}
	if path == "/" {
		return fsError(msg.ID, "refusing to remove /")
	}
	if err := os.RemoveAll(path); err != nil {
		return fsError(msg.ID, err.Error())
	}
	return fsPacket{Type: "fs_ok", ID: msg.ID, Path: path}
}

func fsRename(msg fsPacket) fsPacket {
	from, err := resolveFSPath(msg.Path)
	if err != nil {
		return fsError(msg.ID, err.Error())
	}
	to, err := resolveFSPath(msg.NewPath)
	if err != nil {
		return fsError(msg.ID, err.Error())
	}
	if err := os.Rename(from, to); err != nil {
		return fsError(msg.ID, err.Error())
	}
	return fsPacket{Type: "fs_ok", ID: msg.ID, Path: to}
}
