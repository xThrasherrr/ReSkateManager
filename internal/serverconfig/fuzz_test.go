package serverconfig

import (
	"archive/zip"
	"bytes"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// FuzzInstallMod unpacks zips of arbitrary entries: any names, contents and
// modes, links included. Whatever a zip holds, InstallMod writes nothing
// outside dir/Mods/<folder>, makes no links, keeps to the size and file
// limits, and leaves nothing behind when it refuses.
func FuzzInstallMod(f *testing.F) {
	maxBytes, maxFiles := maxModBytes, maxModFiles
	maxModBytes, maxModFiles = 4<<10, 8
	f.Cleanup(func() { maxModBytes, maxModFiles = maxBytes, maxFiles })

	// names holds one entry per line. Two bits of modes per entry make it a
	// file (0), a folder (1) or a link (2), and data is shared out among them.
	f.Add("bbcity/reskate-levels.json\nbbcity/Levels/a.bin", []byte(levels+"0123456789"), uint64(0), "a.zip")
	f.Add("Mods/bbcity/x\nMods/bbcity/y/z", []byte("12"), uint64(0), "mods.zip")
	f.Add("bbcity\\x\n__MACOSX/bbcity/._x\nbbcity/.DS_Store", []byte("123"), uint64(0), "a.zip")
	// Beside a plain file, so the zip's name names the folder and the bad path
	// is what gets unpacked.
	for _, evil := range []string{"../evil.txt", "bbcity/../../evil.txt", "/etc/evil", `C:\evil`, `..\..\evil.txt`} {
		f.Add("x\n"+evil, []byte("xy"), uint64(0), "a.zip")
	}
	f.Add("bbcity/link\nbbcity/link/x", []byte("../../../x"), uint64(0b0010), "a.zip")
	f.Add("bbcity/\nbbcity/x\nbbcity/x/", []byte("x"), uint64(0b010001), "a.zip")
	f.Add("a/b\na/./b\na//b", []byte("123"), uint64(0), "a.zip")
	f.Add("big", bytes.Repeat([]byte("x"), 5<<10), uint64(0), "big.zip")
	f.Add("1\n2\n3\n4\n5\n6\n7\n8\n9", []byte("123456789"), uint64(0), "many.zip")
	f.Add("x", []byte("1"), uint64(0), `..\..\a.zip`)

	f.Fuzz(func(t *testing.T, names string, data []byte, modes uint64, zipName string) {
		list := strings.Split(names, "\n")
		if len(list) > 32 {
			t.Skip()
		}
		var buf bytes.Buffer
		zw := zip.NewWriter(&buf)
		for i, name := range list {
			h := &zip.FileHeader{Name: name, Method: zip.Store}
			switch (modes >> (2 * i)) & 3 {
			case 1:
				h.SetMode(fs.ModeDir | 0o755)
			case 2:
				h.SetMode(fs.ModeSymlink | 0o777)
			default:
				h.SetMode(0o644)
			}
			w, err := zw.CreateHeader(h)
			if err != nil {
				t.Skip()
			}
			w.Write(data[i*len(data)/len(list) : (i+1)*len(data)/len(list)])
		}
		if err := zw.Close(); err != nil {
			t.Skip()
		}
		zipPath := filepath.Join(t.TempDir(), "upload.zip")
		if err := os.WriteFile(zipPath, buf.Bytes(), 0o644); err != nil {
			t.Fatal(err)
		}

		base := t.TempDir()
		dir := filepath.Join(base, "server")
		if err := os.Mkdir(dir, 0o755); err != nil {
			t.Fatal(err)
		}
		folder, _, err := InstallMod(dir, zipPath, zipName)
		checkInstall(t, base, dir, folder, err)
		if err != nil {
			return
		}
		again, replaced, err := InstallMod(dir, zipPath, zipName)
		if err != nil || again != folder || !replaced {
			t.Fatalf("installing it again: %q, %v, %v; want %q replaced", again, replaced, err, folder)
		}
		checkInstall(t, base, dir, folder, nil)
	})
}

// checkInstall walks base, the server folder's parent. After a refusal the
// server folder holds no files; after an install only dir/Mods/folder does.
func checkInstall(t *testing.T, base, dir, folder string, err error) {
	t.Helper()
	mod := filepath.Join(ModsDir(dir), folder)
	var size int64
	var files int
	walkErr := filepath.WalkDir(base, func(p string, d fs.DirEntry, werr error) error {
		if werr != nil {
			return werr
		}
		switch {
		case d.Type()&fs.ModeSymlink != 0:
			t.Errorf("made a link: %s", p)
		case p == base || p == dir || p == ModsDir(dir):
		case err == nil && (p == mod || strings.HasPrefix(p, mod+string(filepath.Separator))):
			if d.Type().IsRegular() {
				info, ierr := d.Info()
				if ierr != nil {
					return ierr
				}
				size += info.Size()
				files++
			}
		default:
			t.Errorf("left %s behind (install error: %v)", p, err)
		}
		return nil
	})
	if walkErr != nil {
		t.Fatal(walkErr)
	}
	if err != nil {
		return
	}
	if !ValidModFolder(folder) {
		t.Errorf("installed into %q", folder)
	}
	if files == 0 || files > maxModFiles {
		t.Errorf("installed %d files, limit %d", files, maxModFiles)
	}
	if size > maxModBytes {
		t.Errorf("installed %d bytes, limit %d", size, maxModBytes)
	}
}
