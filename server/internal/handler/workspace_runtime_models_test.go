package handler

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	db "github.com/multica-ai/multica/server/pkg/db/generated"
)

func TestInitiateListModels_ConfiguredCatalogReturnsImmediately(t *testing.T) {
	ctx := context.Background()
	withModelListStores(t)

	var runtimeID string
	if err := testPool.QueryRow(ctx, `
		INSERT INTO agent_runtime (
			workspace_id, daemon_id, name, runtime_mode, provider, status,
			device_info, metadata, owner_id, last_seen_at
		)
		VALUES ($1, NULL, 'Configured Catalog Test Runtime', 'cloud', 'codex', 'online',
			'test', '{}'::jsonb, $2, now())
		RETURNING id
	`, testWorkspaceID, testUserID).Scan(&runtimeID); err != nil {
		t.Fatalf("insert runtime: %v", err)
	}
	modelID := "configured-immediate-model-test"
	if _, err := testPool.Exec(ctx, `
		INSERT INTO workspace_runtime_model (
			workspace_id, runtime_provider, model_id, display_name, model_provider,
			description, thinking_levels, default_thinking_level, service_tiers,
			supports_explicit_standard_service_tier, enabled, sort_order
		)
		VALUES ($1, 'codex', $2, 'Configured Immediate Model', 'test', '',
			'[{"value":"low","label":"Low"}]'::jsonb, 'low', '[]'::jsonb,
			false, true, 9999)
		ON CONFLICT (workspace_id, runtime_provider, model_id)
		DO UPDATE SET enabled = true
	`, testWorkspaceID, modelID); err != nil {
		t.Fatalf("insert configured model: %v", err)
	}
	t.Cleanup(func() {
		testPool.Exec(context.Background(), `DELETE FROM workspace_runtime_model WHERE workspace_id = $1 AND runtime_provider = 'codex' AND model_id = $2`, testWorkspaceID, modelID)
		testPool.Exec(context.Background(), `DELETE FROM agent_runtime WHERE id = $1`, runtimeID)
	})

	originalPendingWork := testHandler.DaemonPendingWork
	recorder := &pendingWorkRecorder{}
	testHandler.DaemonPendingWork = recorder
	t.Cleanup(func() { testHandler.DaemonPendingWork = originalPendingWork })

	req := withURLParam(newRequest(http.MethodPost, "/api/runtimes/"+runtimeID+"/models", nil), "runtimeId", runtimeID)
	w := httptest.NewRecorder()
	testHandler.InitiateListModels(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", w.Code, w.Body.String())
	}

	var response ModelListRequest
	if err := json.Unmarshal(w.Body.Bytes(), &response); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if response.Status != ModelListCompleted || !response.Supported || !response.Cached {
		t.Fatalf("expected an immediately completed configured catalog, got %+v", response)
	}
	var found bool
	for _, model := range response.Models {
		if model.ID == modelID {
			found = true
			break
		}
	}
	if !found {
		t.Fatalf("configured model missing from immediate catalog: %+v", response.Models)
	}
	pending, err := testHandler.ModelListStore.HasPending(ctx, runtimeID)
	if err != nil {
		t.Fatalf("check background discovery: %v", err)
	}
	if !pending || recorder.count() != 1 {
		t.Fatalf("expected one background daemon discovery: pending=%v hints=%d", pending, recorder.count())
	}
}

func TestNormalizeWorkspaceRuntimeModelInput(t *testing.T) {
	stringPtr := func(value string) *string { return &value }
	int32Ptr := func(value int32) *int32 { return &value }

	t.Run("normalizes a valid model", func(t *testing.T) {
		thinkingLevels := []ThinkingLevel{{Value: " high ", Label: " High ", Description: " Best quality "}}
		serviceTiers := []ModelServiceTier{{ID: " fast ", Name: " Fast ", Description: " Lower latency "}}
		input, err := normalizeWorkspaceRuntimeModelInput(workspaceRuntimeModelRequest{
			RuntimeProvider:      stringPtr(" codex "),
			ModelID:              stringPtr(" grok-4.6 "),
			DisplayName:          stringPtr(" Grok 4.6 "),
			ModelProvider:        stringPtr(" openai "),
			Description:          stringPtr(" Workspace gateway model "),
			ThinkingLevels:       &thinkingLevels,
			DefaultThinkingLevel: stringPtr(" high "),
			ServiceTiers:         &serviceTiers,
			SortOrder:            int32Ptr(20),
		}, nil)
		if err != nil {
			t.Fatalf("normalize: %v", err)
		}
		if input.RuntimeProvider != "codex" || input.ModelID != "grok-4.6" || input.DisplayName != "Grok 4.6" {
			t.Fatalf("top-level fields were not normalized: %+v", input)
		}
		if input.DefaultThinkingLevel != "high" || input.ThinkingLevels[0].Value != "high" || input.ThinkingLevels[0].Label != "High" {
			t.Fatalf("thinking configuration was not normalized: %+v", input)
		}
		if input.ServiceTiers[0].ID != "fast" || input.ServiceTiers[0].Name != "Fast" {
			t.Fatalf("service tier was not normalized: %+v", input.ServiceTiers)
		}
	})

	t.Run("rejects a default outside the supported levels", func(t *testing.T) {
		thinkingLevels := []ThinkingLevel{{Value: "high", Label: "High"}}
		_, err := normalizeWorkspaceRuntimeModelInput(workspaceRuntimeModelRequest{
			ModelID:              stringPtr("grok-4.6"),
			DisplayName:          stringPtr("Grok 4.6"),
			ThinkingLevels:       &thinkingLevels,
			DefaultThinkingLevel: stringPtr("medium"),
		}, nil)
		if err == nil {
			t.Fatal("expected an invalid default thinking level error")
		}
	})

	t.Run("rejects duplicate thinking levels", func(t *testing.T) {
		thinkingLevels := []ThinkingLevel{
			{Value: "high", Label: "High"},
			{Value: "high", Label: "High again"},
		}
		_, err := normalizeWorkspaceRuntimeModelInput(workspaceRuntimeModelRequest{
			ModelID:        stringPtr("grok-4.6"),
			DisplayName:    stringPtr("Grok 4.6"),
			ThinkingLevels: &thinkingLevels,
		}, nil)
		if err == nil {
			t.Fatal("expected a duplicate thinking level error")
		}
	})
}

func TestMergeWorkspaceRuntimeModels(t *testing.T) {
	thinkingLevels, err := json.Marshal([]ThinkingLevel{{Value: "high", Label: "High"}})
	if err != nil {
		t.Fatalf("marshal thinking levels: %v", err)
	}
	serviceTiers, err := json.Marshal([]ModelServiceTier{{ID: "fast", Name: "Fast"}})
	if err != nil {
		t.Fatalf("marshal service tiers: %v", err)
	}

	discovered := []ModelEntry{
		{ID: "grok-4.6", Label: "Discovered Grok", Provider: "xai", Default: true},
		{ID: "gpt-5.6-sol", Label: "GPT-5.6-Sol", Provider: "openai"},
		{ID: "grok-4.6", Label: "Duplicate Grok", Provider: "duplicate"},
	}
	configured := []db.WorkspaceRuntimeModel{
		{
			ModelID:              "grok-4.6",
			DisplayName:          "Grok 4.6",
			ModelProvider:        "openai",
			ThinkingLevels:       thinkingLevels,
			DefaultThinkingLevel: "high",
			ServiceTiers:         serviceTiers,
		},
		{
			ModelID:              "grok-4.5",
			DisplayName:          "Grok 4.5",
			ModelProvider:        "openai",
			ThinkingLevels:       thinkingLevels,
			DefaultThinkingLevel: "high",
			ServiceTiers:         serviceTiers,
		},
	}

	models := mergeWorkspaceRuntimeModels(discovered, configured)
	if len(models) != 3 {
		t.Fatalf("expected two discovered models plus one configured model, got %+v", models)
	}
	if models[0].Label != "Discovered Grok" || models[0].Provider != "xai" || !models[0].Default {
		t.Fatalf("the first daemon model should win when IDs overlap: %+v", models[0])
	}
	if models[0].Thinking != nil || len(models[0].ServiceTiers) != 0 {
		t.Fatalf("configured metadata should not overwrite a daemon model: %+v", models[0])
	}
	if models[2].ID != "grok-4.5" || models[2].Default {
		t.Fatalf("new configured model should be appended without becoming the runtime default: %+v", models[2])
	}
	if models[2].Thinking == nil || models[2].Thinking.DefaultLevel != "high" || models[2].Thinking.SupportedLevels[0].Value != "high" {
		t.Fatalf("configured thinking levels were not included: %+v", models[2].Thinking)
	}
	if len(models[2].ServiceTiers) != 1 || models[2].ServiceTiers[0].ID != "fast" {
		t.Fatalf("configured service tiers were not included: %+v", models[2].ServiceTiers)
	}
	if discovered[0].Label != "Discovered Grok" || discovered[0].Provider != "xai" {
		t.Fatalf("merge mutated the discovered catalog: %+v", discovered[0])
	}
}

func TestRuntimeModelCatalogBase(t *testing.T) {
	t.Run("repairs an empty supported Codex catalog", func(t *testing.T) {
		models := runtimeModelCatalogBase("codex", nil, true)
		if len(models) == 0 {
			t.Fatal("expected the Codex fallback catalog")
		}

		var sol *ModelEntry
		for i := range models {
			if models[i].ID == "gpt-5.6-sol" {
				sol = &models[i]
			}
			if models[i].ID == "grok-4.6" || models[i].ID == "grok-4.5" {
				t.Fatalf("workspace-configured models must not be hardcoded into the fallback: %+v", models[i])
			}
		}
		if sol == nil || sol.Thinking == nil || len(sol.Thinking.SupportedLevels) == 0 {
			t.Fatalf("expected the fallback to preserve Codex thinking metadata: %+v", sol)
		}
	})

	t.Run("keeps a discovered catalog authoritative", func(t *testing.T) {
		discovered := []ModelEntry{{ID: "account-model", Label: "Account model"}}
		models := runtimeModelCatalogBase("codex", discovered, true)
		if len(models) != 1 || models[0].ID != "account-model" {
			t.Fatalf("expected the discovered catalog unchanged, got %+v", models)
		}
	})

	t.Run("does not invent catalogs for unsupported or other runtimes", func(t *testing.T) {
		if models := runtimeModelCatalogBase("codex", nil, false); len(models) != 0 {
			t.Fatalf("unsupported Codex runtime should remain empty, got %+v", models)
		}
		if models := runtimeModelCatalogBase("claude", nil, true); len(models) != 0 {
			t.Fatalf("non-Codex runtime should remain empty, got %+v", models)
		}
	})
}
