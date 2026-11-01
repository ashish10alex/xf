package trash

import (
	"encoding/binary"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"unicode/utf16"

	"golang.org/x/sys/windows"
)

// A file goes to the Recycle Bin, which says where it came from. The test
// takes it out of the bin again.
func TestMoveWindows(t *testing.T) {
	dir := t.TempDir()
	for _, n := range []string{"my café.sqlx", "includes"} {
		p := filepath.Join(dir, n)
		var err error
		if filepath.Ext(n) == "" {
			err = os.Mkdir(p, 0o755)
		} else {
			err = os.WriteFile(p, []byte("x"), 0o644)
		}
		if err != nil {
			t.Fatal(err)
		}
		if err := Move(p); err != nil {
			t.Fatal(err)
		}
		if _, err := os.Lstat(p); !os.IsNotExist(err) {
			t.Fatalf("%s is still there", p)
		}
		if !inBin(t, p) {
			t.Errorf("%s isn't in the Recycle Bin", p)
		}
	}
}

// inBin finds path in its volume's Recycle Bin, by the $I file that holds
// where each $R file came from, and removes both.
func inBin(t *testing.T, path string) bool {
	t.Helper()
	path = filepath.Join(longPath(t, filepath.Dir(path)), filepath.Base(path)) // path itself is gone
	tok := windows.GetCurrentProcessToken()
	u, err := tok.GetTokenUser()
	if err != nil {
		t.Fatal(err)
	}
	bin := filepath.Join(filepath.VolumeName(path)+`\`, "$Recycle.Bin", u.User.Sid.String())
	is, _ := filepath.Glob(filepath.Join(bin, "$I*"))
	for _, i := range is {
		b, err := os.ReadFile(i)
		if err != nil || len(b) < 28 || binary.LittleEndian.Uint64(b) != 2 {
			continue
		}
		u16 := make([]uint16, (len(b)-28)/2)
		for k := range u16 {
			u16[k] = binary.LittleEndian.Uint16(b[28+2*k:])
		}
		if strings.TrimRight(string(utf16.Decode(u16)), "\x00") != path {
			continue
		}
		r := filepath.Join(bin, "$R"+filepath.Base(i)[2:])
		if err := os.RemoveAll(r); err != nil {
			t.Error(err)
		}
		_ = os.Remove(i)
		return true
	}
	return false
}

// longPath is path with its 8.3 short names (RUNNER~1) spelled out, as the
// Recycle Bin records it; t.TempDir on a CI runner is under one.
func longPath(t *testing.T, path string) string {
	t.Helper()
	from, err := windows.UTF16PtrFromString(path)
	if err != nil {
		t.Fatal(err)
	}
	buf := make([]uint16, windows.MAX_LONG_PATH)
	n, err := windows.GetLongPathName(from, &buf[0], uint32(len(buf)))
	if err != nil || int(n) > len(buf) {
		return path // too long to expand: compare as it is
	}
	return windows.UTF16ToString(buf[:n])
}

// Over a network path there is no Recycle Bin, and the shell would delete
// for good: Move refuses and leaves the file.
func TestMoveNoBin(t *testing.T) {
	dir := t.TempDir()
	f := filepath.Join(dir, "shared.sqlx")
	if err := os.WriteFile(f, []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	vol := filepath.VolumeName(f)
	unc := `\\localhost\` + strings.TrimSuffix(vol, ":") + "$" + f[len(vol):]
	if _, err := os.Lstat(unc); err != nil {
		t.Skip("no admin share:", err)
	}
	if err := Move(unc); err == nil || !strings.Contains(err.Error(), "no Recycle Bin") {
		t.Errorf("Move over the network: %v", err)
	}
	if _, err := os.Lstat(f); err != nil {
		t.Errorf("the file is gone: %v", err)
	}
}
