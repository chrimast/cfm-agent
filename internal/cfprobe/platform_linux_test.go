//go:build linux

package cfprobe

import (
	"path/filepath"
	"testing"
)

func TestUserModeInstallPaths(t *testing.T) {
	paths := userModeInstallPaths("/opt/cfm")
	want := []string{
		"/opt/cfm/.config/systemd/user/cf-probe.service",
		"/opt/cfm/.cf-probe/bin/cf-probe",
		"/opt/cfm/.cf-probe/config.conf",
		"/opt/cfm/.cf-probe",
	}
	if len(paths) != len(want) {
		t.Fatalf("len=%d want %d: %v", len(paths), len(want), paths)
	}
	for i := range want {
		if filepath.ToSlash(paths[i]) != want[i] {
			t.Fatalf("paths[%d]=%q want %q", i, paths[i], want[i])
		}
	}
}

func TestParsePasswdUserHomeDirsIncludesCfm(t *testing.T) {
	raw := "root:x:0:0:root:/root:/bin/bash\ncfm:x:1000:1000::/opt/cfm:/bin/bash\nnobody:x:65534:65534::/nonexistent:/usr/sbin/nologin\n"
	homes := parsePasswdUserHomeDirs(raw)
	if len(homes) != 1 || homes[0] != "/opt/cfm" {
		t.Fatalf("homes=%v, want [/opt/cfm]", homes)
	}
}
