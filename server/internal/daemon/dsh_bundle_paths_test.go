package daemon

import (
	"path/filepath"
	"reflect"
	"testing"
)

func envFrom(values map[string]string) func(string) string {
	return func(key string) string { return values[key] }
}

func TestDshDesktopBundlePaths(t *testing.T) {
	const bundle = "DeepSeek Harness.app/Contents/Resources/runtime/cli/bin/dsh"
	for _, test := range []struct {
		name, goos, home string
		env              map[string]string
		want             []string
	}{
		{name: "macOS", goos: "darwin", home: "/test-home", want: []string{filepath.Join("/Applications", bundle), filepath.Join("/test-home/Applications", bundle)}},
		{name: "no home", goos: "darwin", want: []string{filepath.Join("/Applications", bundle)}},
		{name: "Windows", goos: "windows", env: map[string]string{"LOCALAPPDATA": "local", "ProgramFiles": "system", "ProgramFiles(x86)": "system32"}, want: []string{
			filepath.Join("local", "Programs", "DeepSeek Harness", "resources", "runtime", "cli", "bin", "dsh.cmd"),
			filepath.Join("system", "DeepSeek Harness", "resources", "runtime", "cli", "bin", "dsh.cmd"),
			filepath.Join("system32", "DeepSeek Harness", "resources", "runtime", "cli", "bin", "dsh.cmd"),
		}},
		{name: "Linux uses PATH", goos: "linux", want: []string{}},
	} {
		t.Run(test.name, func(t *testing.T) {
			if got := dshDesktopBundlePathsFor(test.goos, envFrom(test.env), test.home); !reflect.DeepEqual(got, test.want) {
				t.Fatalf("got %v, want %v", got, test.want)
			}
		})
	}
}
