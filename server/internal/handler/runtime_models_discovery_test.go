package handler

import (
	"context"
	"net/http"
	"testing"

	"github.com/multica-ai/multica/server/internal/testutil"
)

// 2026-10-09 coder(lq): A live-discovery request must not succeed merely because an operator configured model candidates.
func TestForcedModelDiscoveryRequiresDaemonResult(t *testing.T) {
	ctx := context.Background()
	fx := testutil.New(testPool, testWorkspaceID, testUserID)
	runtimeID := fx.Runtime(t, "DSH discovery", testutil.Cols{"provider": "dsh"})
	fx.Insert(t, "workspace_runtime_model", testutil.Cols{
		"workspace_id": testWorkspaceID, "runtime_provider": "codex",
		"model_id": "operator-candidate", "display_name": "Operator candidate",
	})
	for _, warm := range []bool{false, true} {
		t.Run(map[bool]string{false: "configured only", true: "cached catalog"}[warm], func(t *testing.T) {
			cache := withModelListStores(t)
			if warm {
				if err := cache.Put(ctx, runtimeID, []ModelEntry{{ID: "old-runtime-model", Label: "Old model"}}, true); err != nil {
					t.Fatal(err)
				}
			}
			req := withURLParam(newRequest(http.MethodPost, "/api/runtimes/"+runtimeID+"/models?force=true", nil), "runtimeId", runtimeID)
			var initial ModelListRequest
			testutil.Call(t, testHandler.InitiateListModels, req).Want(http.StatusOK).JSON(&initial)
			if initial.Status != ModelListPending || initial.Cached || len(initial.Models) != 0 {
				t.Fatalf("live discovery must wait for the daemon, got %+v", initial)
			}
			claimed, err := testHandler.ModelListStore.PopPending(ctx, runtimeID)
			if err != nil || claimed == nil || claimed.ID != initial.ID {
				t.Fatalf("request was not queued for the daemon: %+v %v", claimed, err)
			}
			actualID := `["deepseek-official","deepseek-flash"]`
			if err := testHandler.ModelListStore.Complete(ctx, initial.ID, []ModelEntry{{ID: actualID, Label: "Flash"}}, true); err != nil {
				t.Fatal(err)
			}
			poll := testutil.WithURLParams(newRequest(http.MethodGet, "/api/runtimes/"+runtimeID+"/models/"+initial.ID+"?force=true", nil), "runtimeId", runtimeID, "requestId", initial.ID)
			var result ModelListRequest
			testutil.Call(t, testHandler.GetModelListRequest, poll).Want(http.StatusOK).JSON(&result)
			if result.Status != ModelListCompleted || len(result.Models) != 1 || result.Models[0].ID != actualID {
				t.Fatalf("live result must contain only literal daemon IDs, got %+v", result)
			}
			failed, err := testHandler.ModelListStore.Create(ctx, runtimeID)
			if err != nil {
				t.Fatal(err)
			}
			if err := testHandler.ModelListStore.Fail(ctx, failed.ID, "DSH discovery failed"); err != nil {
				t.Fatal(err)
			}
			failurePoll := testutil.WithURLParams(newRequest(http.MethodGet, "/api/runtimes/"+runtimeID+"/models/"+failed.ID+"?force=true", nil), "runtimeId", runtimeID, "requestId", failed.ID)
			var failure ModelListRequest
			testutil.Call(t, testHandler.GetModelListRequest, failurePoll).Want(http.StatusOK).JSON(&failure)
			if failure.Status != ModelListFailed || failure.Error != "DSH discovery failed" || len(failure.Models) != 0 {
				t.Fatalf("configured candidates must not hide a discovery failure: %+v", failure)
			}
		})
	}
	offlineID := fx.Runtime(t, "Offline DSH discovery", testutil.Cols{"provider": "dsh", "status": "offline"})
	offline := testutil.WithURLParams(newRequest(http.MethodPost, "/api/runtimes/"+offlineID+"/models?force=true", nil), "runtimeId", offlineID)
	testutil.Call(t, testHandler.InitiateListModels, offline).Want(http.StatusServiceUnavailable)
}
