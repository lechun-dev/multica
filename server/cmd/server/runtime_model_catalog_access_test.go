package main

import (
	"io"
	"net/http"
	"testing"

	"github.com/google/uuid"
	"github.com/multica-ai/multica/server/internal/testutil"
)

func TestRuntimeModelCatalogMemberReadOwnerWrite(t *testing.T) {
	fx := testutil.New(testPool, testWorkspaceID, testUserID)
	memberID := fx.User(t, "Catalog reader", uuid.NewString()+"@example.test")
	fx.Member(t, testWorkspaceID, memberID, "member")
	outsiderID := fx.User(t, "Catalog outsider", uuid.NewString()+"@example.test")
	memberToken, err := generateTestJWT(memberID, "reader@example.test", "Catalog reader")
	if err != nil {
		t.Fatal(err)
	}
	outsiderToken, err := generateTestJWT(outsiderID, "outsider@example.test", "Catalog outsider")
	if err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		name, method, suffix, token string
		status                      int
	}{
		{"member reads", http.MethodGet, "", memberToken, http.StatusOK},
		{"member cannot create", http.MethodPost, "", memberToken, http.StatusForbidden},
		{"member cannot edit", http.MethodPatch, "/" + uuid.NewString(), memberToken, http.StatusForbidden},
		{"member cannot delete", http.MethodDelete, "/" + uuid.NewString(), memberToken, http.StatusForbidden},
		{"outsider cannot read", http.MethodGet, "", outsiderToken, http.StatusNotFound},
		{"anonymous cannot read", http.MethodGet, "", "", http.StatusUnauthorized},
	} {
		t.Run(tc.name, func(t *testing.T) {
			req, err := http.NewRequest(tc.method, testServer.URL+"/api/workspaces/"+testWorkspaceID+"/runtime-models"+tc.suffix, nil)
			if err != nil {
				t.Fatal(err)
			}
			if tc.token != "" {
				req.Header.Set("Authorization", "Bearer "+tc.token)
			}
			req.Header.Set("X-Workspace-ID", testWorkspaceID)
			resp, err := http.DefaultClient.Do(req)
			if err != nil {
				t.Fatal(err)
			}
			defer resp.Body.Close()
			body, _ := io.ReadAll(resp.Body)
			if resp.StatusCode != tc.status {
				t.Fatalf("status=%d want=%d body=%s", resp.StatusCode, tc.status, body)
			}
		})
	}
}
