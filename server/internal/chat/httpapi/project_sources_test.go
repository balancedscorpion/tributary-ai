package httpapi

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"ai-analyst-local/internal/chat/projects"
	"ai-analyst-local/internal/chat/store"
)

func TestProjectSourcesHideAndDelete(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	cfgDir := t.TempDir()

	st, err := store.NewJSON(cfgDir)
	if err != nil {
		t.Fatalf("store.NewJSON: %v", err)
	}
	ps, err := projects.NewStore(cfgDir)
	if err != nil {
		t.Fatalf("projects.NewStore: %v", err)
	}
	h := &Handler{
		store:        st,
		projects:     ps,
		cfgDir:       cfgDir,
		maxBodyBytes: 1 << 20,
	}

	project, err := st.CreateProject(ctx, "Project", "projects")
	if err != nil {
		t.Fatalf("CreateProject: %v", err)
	}
	if err := ps.PutFile(project.ID, "alpha/a.txt", []byte("alpha secret")); err != nil {
		t.Fatalf("PutFile alpha: %v", err)
	}
	if err := ps.PutFile(project.ID, "beta/b.txt", []byte("beta visible")); err != nil {
		t.Fatalf("PutFile beta: %v", err)
	}
	project, err = h.refreshProjectFileIndex(ctx, project.ID)
	if err != nil {
		t.Fatalf("refreshProjectFileIndex: %v", err)
	}
	if project.FileCount != 2 {
		t.Fatalf("initial file_count = %d, want 2", project.FileCount)
	}

	hideReq := httptest.NewRequest(http.MethodPatch, "/api/projects/"+project.ID+"/sources/alpha", strings.NewReader(`{"hidden":true}`))
	hideReq.SetPathValue("projectId", project.ID)
	hideReq.SetPathValue("sourceName", "alpha")
	hideRec := httptest.NewRecorder()
	h.handleUpdateProjectSource(hideRec, hideReq)
	if hideRec.Code != http.StatusOK {
		t.Fatalf("hide source status = %d, body = %s", hideRec.Code, hideRec.Body.String())
	}
	var hideResp struct {
		Project struct {
			FileCount int `json:"file_count"`
		} `json:"project"`
		Sources []struct {
			Name   string `json:"name"`
			Hidden bool   `json:"hidden"`
		} `json:"sources"`
	}
	if err := json.Unmarshal(hideRec.Body.Bytes(), &hideResp); err != nil {
		t.Fatalf("decode hide response: %v", err)
	}
	if hideResp.Project.FileCount != 1 {
		t.Fatalf("file_count after hide = %d, want 1", hideResp.Project.FileCount)
	}
	if !sourceHidden(hideResp.Sources, "alpha") {
		t.Fatalf("alpha source was not marked hidden: %#v", hideResp.Sources)
	}
	if _, _, err := ps.ReadFile(project.ID, "alpha/a.txt", 1024); err == nil {
		t.Fatal("ReadFile hidden source error = nil, want error")
	}
	if _, _, err := ps.ReadFile(project.ID, "beta/b.txt", 1024); err != nil {
		t.Fatalf("ReadFile visible source: %v", err)
	}

	deleteReq := httptest.NewRequest(http.MethodDelete, "/api/projects/"+project.ID+"/sources/beta", nil)
	deleteReq.SetPathValue("projectId", project.ID)
	deleteReq.SetPathValue("sourceName", "beta")
	deleteRec := httptest.NewRecorder()
	h.handleDeleteProjectSource(deleteRec, deleteReq)
	if deleteRec.Code != http.StatusOK {
		t.Fatalf("delete source status = %d, body = %s", deleteRec.Code, deleteRec.Body.String())
	}
	var deleteResp struct {
		Project struct {
			FileCount int `json:"file_count"`
		} `json:"project"`
		Sources []struct {
			Name string `json:"name"`
		} `json:"sources"`
	}
	if err := json.Unmarshal(deleteRec.Body.Bytes(), &deleteResp); err != nil {
		t.Fatalf("decode delete response: %v", err)
	}
	if deleteResp.Project.FileCount != 0 {
		t.Fatalf("file_count after delete = %d, want 0", deleteResp.Project.FileCount)
	}
	if sourceExists(deleteResp.Sources, "beta") {
		t.Fatalf("beta source still listed after delete: %#v", deleteResp.Sources)
	}
	if _, err := os.Stat(filepath.Join(cfgDir, "projects", project.ID, "files", "beta")); !os.IsNotExist(err) {
		t.Fatalf("deleted source stat error = %v, want not exist", err)
	}
	if _, err := os.Stat(filepath.Join(cfgDir, "projects", project.ID, "files", "alpha")); err != nil {
		t.Fatalf("hidden source should remain on disk: %v", err)
	}
}

func sourceHidden(sources []struct {
	Name   string `json:"name"`
	Hidden bool   `json:"hidden"`
}, name string) bool {
	for _, source := range sources {
		if source.Name == name {
			return source.Hidden
		}
	}
	return false
}

func sourceExists(sources []struct {
	Name string `json:"name"`
}, name string) bool {
	for _, source := range sources {
		if source.Name == name {
			return true
		}
	}
	return false
}
