package main

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/AltairaLabs/PromptKit/runtime/prompt"
)

func TestResolveAgentName_EnvOverride(t *testing.T) {
	cfg := &runtimeConfig{AgentName: "override"}
	pack := &prompt.Pack{
		Agents: &prompt.AgentsConfig{Entry: "entry-agent"},
	}

	name, err := resolveAgentName(cfg, pack)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if name != "override" {
		t.Errorf("name = %q, want %q", name, "override")
	}
}

func TestResolveAgentName_AgentsEntry(t *testing.T) {
	cfg := &runtimeConfig{}
	pack := &prompt.Pack{
		Agents: &prompt.AgentsConfig{Entry: "orchestrator"},
	}

	name, err := resolveAgentName(cfg, pack)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if name != "orchestrator" {
		t.Errorf("name = %q, want %q", name, "orchestrator")
	}
}

func TestResolveAgentName_SinglePrompt(t *testing.T) {
	cfg := &runtimeConfig{}
	pack := &prompt.Pack{
		Prompts: map[string]*prompt.PackPrompt{
			"chat": {Name: "chat"},
		},
	}

	name, err := resolveAgentName(cfg, pack)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if name != "chat" {
		t.Errorf("name = %q, want %q", name, "chat")
	}
}

func TestResolveAgentName_Ambiguous(t *testing.T) {
	cfg := &runtimeConfig{}
	pack := &prompt.Pack{
		Prompts: map[string]*prompt.PackPrompt{
			"a": {Name: "a"},
			"b": {Name: "b"},
		},
	}

	_, err := resolveAgentName(cfg, pack)
	if err == nil {
		t.Fatal("expected error for ambiguous prompts")
	}
}

func TestBuildMux_HealthRoute(t *testing.T) {
	a2aHandler := http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	})
	healthH := newHealthHandler()
	mux := buildMux(a2aHandler, healthH)

	// Test /health returns 200
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/health", nil)
	mux.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Errorf("/health status = %d, want %d", rec.Code, http.StatusOK)
	}
}

func TestBuildMux_RootRoute(t *testing.T) {
	called := false
	a2aHandler := http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		called = true
		w.WriteHeader(http.StatusOK)
	})
	healthH := newHealthHandler()
	mux := buildMux(a2aHandler, healthH)

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/a2a", nil)
	mux.ServeHTTP(rec, req)
	if !called {
		t.Error("expected root handler to be called for /a2a")
	}
}

func TestBuildAgentCard_FromPack(t *testing.T) {
	pack := &prompt.Pack{
		Version: "1.0.0",
		Agents: &prompt.AgentsConfig{
			Entry: "myagent",
			Members: map[string]*prompt.AgentDef{
				"myagent": {
					Description: "test agent",
					Tags:        []string{"test"},
				},
			},
		},
		Prompts: map[string]*prompt.PackPrompt{
			"myagent": {
				Name:        "myagent",
				Description: "test agent prompt",
			},
		},
	}

	card := buildAgentCard(pack, "myagent")
	if card.Name != "myagent" {
		t.Errorf("card.Name = %q, want %q", card.Name, "myagent")
	}
}

func TestBuildAgentCard_Fallback(t *testing.T) {
	pack := &prompt.Pack{
		Version: "2.0.0",
		Prompts: map[string]*prompt.PackPrompt{
			"chat": {
				Name:        "chat",
				Description: "a chat prompt",
			},
		},
	}

	card := buildAgentCard(pack, "chat")
	if card.Name != "chat" {
		t.Errorf("card.Name = %q, want %q", card.Name, "chat")
	}
	if card.Description != "a chat prompt" {
		t.Errorf("card.Description = %q, want %q", card.Description, "a chat prompt")
	}
	if card.Version != "2.0.0" {
		t.Errorf("card.Version = %q, want %q", card.Version, "2.0.0")
	}
}

func TestBuildSDKOptions_WithRegionAndEndpoints(t *testing.T) {
	cfg := &runtimeConfig{
		AWSRegion: "us-west-2",
		AgentEndpoints: map[string]string{
			"sub": "http://sub:9000",
		},
	}

	opts := buildSDKOptions(cfg)
	// 3 options: WithBedrock, WithStateStore, WithAgentEndpoints
	if len(opts) != 3 {
		t.Errorf("expected 3 options, got %d", len(opts))
	}
}

func TestBuildSDKOptions_NoRegion(t *testing.T) {
	cfg := &runtimeConfig{}

	opts := buildSDKOptions(cfg)
	// 1 option: WithStateStore only
	if len(opts) != 1 {
		t.Errorf("expected 1 option, got %d", len(opts))
	}
}
