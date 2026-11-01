//go:build windows

package watch

import (
	"testing"

	"github.com/fsnotify/fsnotify"
)

// A root file's event counts whatever case the watcher reports the
// directory in: on Windows c:\proj and C:\Proj are one directory.
func TestRelevantIgnoresCaseOnWindows(t *testing.T) {
	s := Spec{RootFiles: []string{"workflow_settings.yaml"}}
	if !s.relevant(`C:\Proj`, fsnotify.Event{Name: `c:\proj\workflow_settings.yaml`, Op: fsnotify.Write}) {
		t.Error("an edit to workflow_settings.yaml under another case of the root was ignored")
	}
}
