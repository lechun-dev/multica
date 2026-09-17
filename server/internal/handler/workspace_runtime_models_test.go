package handler

import (
	"encoding/json"
	"testing"

	db "github.com/multica-ai/multica/server/pkg/db/generated"
)

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
			ModelID:       "grok-4.5",
			DisplayName:   "Grok 4.5",
			ModelProvider: "openai",
		},
	}

	models := mergeWorkspaceRuntimeModels(discovered, configured)
	if len(models) != 3 {
		t.Fatalf("expected two discovered models plus one configured model, got %+v", models)
	}
	if models[0].Label != "Grok 4.6" || models[0].Provider != "openai" || !models[0].Default {
		t.Fatalf("configured metadata should override while preserving the runtime default: %+v", models[0])
	}
	if models[0].Thinking == nil || models[0].Thinking.DefaultLevel != "high" || models[0].Thinking.SupportedLevels[0].Value != "high" {
		t.Fatalf("configured thinking levels were not merged: %+v", models[0].Thinking)
	}
	if len(models[0].ServiceTiers) != 1 || models[0].ServiceTiers[0].ID != "fast" {
		t.Fatalf("configured service tiers were not merged: %+v", models[0].ServiceTiers)
	}
	if models[2].ID != "grok-4.5" || models[2].Default {
		t.Fatalf("new configured model should be appended without becoming the runtime default: %+v", models[2])
	}
	if discovered[0].Label != "Discovered Grok" || discovered[0].Provider != "xai" {
		t.Fatalf("merge mutated the discovered catalog: %+v", discovered[0])
	}
}
