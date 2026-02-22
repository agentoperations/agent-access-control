package controller

import (
	"testing"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"

	v1alpha1 "github.com/agentoperations/agent-access-control/api/v1alpha1"
)

func testAgentCard(name, namespace string) *v1alpha1.AgentCard {
	return &v1alpha1.AgentCard{
		ObjectMeta: metav1.ObjectMeta{
			Name:      name,
			Namespace: namespace,
			UID:       types.UID("test-uid-card"),
		},
		Spec: v1alpha1.AgentCardSpec{
			Description: "Test agent",
			Protocols:   []string{"a2a", "mcp"},
			ServicePort: 9090,
			Skills: []v1alpha1.AgentSkill{
				{Name: "search", Description: "Search the web"},
			},
		},
	}
}

func testAgentPolicy(name, namespace string) *v1alpha1.AgentPolicy {
	return &v1alpha1.AgentPolicy{
		ObjectMeta: metav1.ObjectMeta{
			Name:      name,
			Namespace: namespace,
			UID:       types.UID("test-uid-policy"),
		},
		Spec: v1alpha1.AgentPolicySpec{
			AgentSelector: v1alpha1.AgentSelector{
				MatchLabels: map[string]string{"tier": "premium"},
			},
			Ingress: &v1alpha1.IngressPolicy{
				AllowedAgents: []string{"agent-a", "agent-b"},
			},
			RateLimit: &v1alpha1.RateLimitSpec{
				RequestsPerMinute: 100,
			},
			Agents: []string{"weather-agent"},
			External: &v1alpha1.ExternalPolicy{
				DefaultMode: "deny",
				Rules: []v1alpha1.ExternalRule{
					{
						Host:         "api.example.com",
						Mode:         "vault",
						VaultPath:    "secret/data/api-key",
						Header:       "Authorization",
						HeaderPrefix: "Bearer ",
					},
				},
			},
		},
	}
}

func TestBuildHTTPRoute(t *testing.T) {
	card := testAgentCard("weather", "default")
	route := BuildHTTPRoute(card, "my-gateway", "gateway-ns")

	t.Run("metadata", func(t *testing.T) {
		if route.Name != "agent-weather" {
			t.Errorf("expected name 'agent-weather', got %q", route.Name)
		}
		if route.Namespace != "default" {
			t.Errorf("expected namespace 'default', got %q", route.Namespace)
		}
	})

	t.Run("labels", func(t *testing.T) {
		if route.Labels[labelManagedBy] != managedByValue {
			t.Errorf("expected label %s=%s, got %q", labelManagedBy, managedByValue, route.Labels[labelManagedBy])
		}
		if route.Labels[labelAgentCard] != "weather" {
			t.Errorf("expected label %s=weather, got %q", labelAgentCard, route.Labels[labelAgentCard])
		}
	})

	t.Run("parent_ref", func(t *testing.T) {
		if len(route.Spec.ParentRefs) != 1 {
			t.Fatalf("expected 1 parent ref, got %d", len(route.Spec.ParentRefs))
		}
		ref := route.Spec.ParentRefs[0]
		if string(ref.Name) != "my-gateway" {
			t.Errorf("expected gateway name 'my-gateway', got %q", ref.Name)
		}
		if ref.Namespace == nil || string(*ref.Namespace) != "gateway-ns" {
			t.Errorf("expected gateway namespace 'gateway-ns'")
		}
	})

	t.Run("route_rule", func(t *testing.T) {
		if len(route.Spec.Rules) != 1 {
			t.Fatalf("expected 1 rule, got %d", len(route.Spec.Rules))
		}
		rule := route.Spec.Rules[0]
		if len(rule.Matches) != 1 || rule.Matches[0].Path == nil {
			t.Fatal("expected 1 match with path")
		}
		if *rule.Matches[0].Path.Value != "/agents/weather" {
			t.Errorf("expected path '/agents/weather', got %q", *rule.Matches[0].Path.Value)
		}
		if len(rule.BackendRefs) != 1 {
			t.Fatalf("expected 1 backend ref, got %d", len(rule.BackendRefs))
		}
		if string(rule.BackendRefs[0].Name) != "weather-svc" {
			t.Errorf("expected backend 'weather-svc', got %q", rule.BackendRefs[0].Name)
		}
		if rule.BackendRefs[0].Port == nil || int(*rule.BackendRefs[0].Port) != 9090 {
			t.Errorf("expected port 9090")
		}
	})

	t.Run("owner_reference", func(t *testing.T) {
		if len(route.OwnerReferences) != 1 {
			t.Fatalf("expected 1 owner ref, got %d", len(route.OwnerReferences))
		}
		ref := route.OwnerReferences[0]
		if ref.APIVersion != "kagenti.com/v1alpha1" {
			t.Errorf("expected owner apiVersion 'kagenti.com/v1alpha1', got %q", ref.APIVersion)
		}
		if ref.Kind != "AgentCard" {
			t.Errorf("expected owner kind 'AgentCard', got %q", ref.Kind)
		}
		if ref.Name != "weather" {
			t.Errorf("expected owner name 'weather', got %q", ref.Name)
		}
	})
}

func TestBuildHTTPRoute_DefaultPort(t *testing.T) {
	card := testAgentCard("agent1", "ns1")
	card.Spec.ServicePort = 0

	route := BuildHTTPRoute(card, "gw", "gw-ns")

	rule := route.Spec.Rules[0]
	if rule.BackendRefs[0].Port == nil || int(*rule.BackendRefs[0].Port) != 8080 {
		t.Errorf("expected default port 8080")
	}
}

func TestBuildAuthPolicy(t *testing.T) {
	card := testAgentCard("weather", "default")
	policy := testAgentPolicy("premium-policy", "default")

	authPolicy := BuildAuthPolicy(policy, card, "agent-weather")

	t.Run("metadata", func(t *testing.T) {
		if authPolicy.GetName() != "ap-weather" {
			t.Errorf("expected name 'ap-weather', got %q", authPolicy.GetName())
		}
		if authPolicy.GetNamespace() != "default" {
			t.Errorf("expected namespace 'default', got %q", authPolicy.GetNamespace())
		}
	})

	t.Run("apiversion_kind", func(t *testing.T) {
		if authPolicy.GetAPIVersion() != "kuadrant.io/v1" {
			t.Errorf("expected apiVersion 'kuadrant.io/v1', got %q", authPolicy.GetAPIVersion())
		}
		if authPolicy.GetKind() != "AuthPolicy" {
			t.Errorf("expected kind 'AuthPolicy', got %q", authPolicy.GetKind())
		}
	})

	t.Run("target_ref", func(t *testing.T) {
		spec := authPolicy.Object["spec"].(map[string]interface{})
		targetRef := spec["targetRef"].(map[string]interface{})
		if targetRef["name"] != "agent-weather" {
			t.Errorf("expected targetRef name 'agent-weather', got %v", targetRef["name"])
		}
		if targetRef["kind"] != "HTTPRoute" {
			t.Errorf("expected targetRef kind 'HTTPRoute', got %v", targetRef["kind"])
		}
	})

	t.Run("owner_reference", func(t *testing.T) {
		refs := authPolicy.GetOwnerReferences()
		if len(refs) != 1 {
			t.Fatalf("expected 1 owner ref, got %d", len(refs))
		}
		if refs[0].APIVersion != "kagenti.com/v1alpha1" {
			t.Errorf("expected owner apiVersion 'kagenti.com/v1alpha1', got %q", refs[0].APIVersion)
		}
		if refs[0].Kind != "AgentPolicy" {
			t.Errorf("expected owner kind 'AgentPolicy', got %q", refs[0].Kind)
		}
	})
}

func TestBuildRateLimitPolicy(t *testing.T) {
	card := testAgentCard("weather", "default")
	policy := testAgentPolicy("premium-policy", "default")

	rlp := BuildRateLimitPolicy(policy, card, "agent-weather")

	t.Run("metadata", func(t *testing.T) {
		if rlp.GetName() != "rlp-weather" {
			t.Errorf("expected name 'rlp-weather', got %q", rlp.GetName())
		}
		if rlp.GetAPIVersion() != "kuadrant.io/v1" {
			t.Errorf("expected apiVersion 'kuadrant.io/v1', got %q", rlp.GetAPIVersion())
		}
	})

	t.Run("rate_limit_value", func(t *testing.T) {
		spec := rlp.Object["spec"].(map[string]interface{})
		limits := spec["limits"].(map[string]interface{})
		agentRL := limits["agent-rate-limit"].(map[string]interface{})
		rates := agentRL["rates"].([]interface{})
		if len(rates) != 1 {
			t.Fatalf("expected 1 rate, got %d", len(rates))
		}
		rate := rates[0].(map[string]interface{})
		if rate["limit"] != int64(100) {
			t.Errorf("expected limit 100, got %v", rate["limit"])
		}
		if rate["window"] != "1m" {
			t.Errorf("expected window '1m', got %v", rate["window"])
		}
	})
}

func TestBuildRateLimitPolicy_DefaultRPM(t *testing.T) {
	card := testAgentCard("agent1", "ns1")
	policy := testAgentPolicy("pol1", "ns1")
	policy.Spec.RateLimit = nil

	rlp := BuildRateLimitPolicy(policy, card, "agent-agent1")

	spec := rlp.Object["spec"].(map[string]interface{})
	limits := spec["limits"].(map[string]interface{})
	agentRL := limits["agent-rate-limit"].(map[string]interface{})
	rates := agentRL["rates"].([]interface{})
	rate := rates[0].(map[string]interface{})
	if rate["limit"] != int64(60) {
		t.Errorf("expected default limit 60, got %v", rate["limit"])
	}
}

func TestBuildSidecarConfigMap(t *testing.T) {
	card := testAgentCard("weather", "default")
	policy := testAgentPolicy("premium-policy", "default")

	cm, err := BuildSidecarConfigMap(policy, card)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	t.Run("metadata", func(t *testing.T) {
		if cm.Name != "sidecar-config-weather" {
			t.Errorf("expected name 'sidecar-config-weather', got %q", cm.Name)
		}
		if cm.Namespace != "default" {
			t.Errorf("expected namespace 'default', got %q", cm.Namespace)
		}
	})

	t.Run("labels", func(t *testing.T) {
		if cm.Labels[labelManagedBy] != managedByValue {
			t.Errorf("expected managed-by label")
		}
		if cm.Labels[labelAgentCard] != "weather" {
			t.Errorf("expected agent-card label")
		}
	})

	t.Run("config_data", func(t *testing.T) {
		data, ok := cm.Data["config.yaml"]
		if !ok {
			t.Fatal("expected 'config.yaml' key in ConfigMap data")
		}
		if len(data) == 0 {
			t.Fatal("expected non-empty config.yaml")
		}
	})

	t.Run("owner_reference", func(t *testing.T) {
		if len(cm.OwnerReferences) != 1 {
			t.Fatalf("expected 1 owner ref, got %d", len(cm.OwnerReferences))
		}
		if cm.OwnerReferences[0].Kind != "AgentPolicy" {
			t.Errorf("expected owner kind 'AgentPolicy', got %q", cm.OwnerReferences[0].Kind)
		}
	})
}

func TestBuildMCPServerRegistration(t *testing.T) {
	card := testAgentCard("weather", "default")

	mcpReg := BuildMCPServerRegistration(card, "agent-weather")

	t.Run("metadata", func(t *testing.T) {
		if mcpReg.GetName() != "mcp-weather" {
			t.Errorf("expected name 'mcp-weather', got %q", mcpReg.GetName())
		}
		if mcpReg.GetAPIVersion() != "mcp.kagenti.com/v1alpha1" {
			t.Errorf("expected apiVersion 'mcp.kagenti.com/v1alpha1', got %q", mcpReg.GetAPIVersion())
		}
	})

	t.Run("spec", func(t *testing.T) {
		spec := mcpReg.Object["spec"].(map[string]interface{})
		if spec["toolPrefix"] != "weather_" {
			t.Errorf("expected toolPrefix 'weather_', got %v", spec["toolPrefix"])
		}
		if spec["path"] != "/mcp" {
			t.Errorf("expected path '/mcp', got %v", spec["path"])
		}
		targetRef := spec["targetRef"].(map[string]interface{})
		if targetRef["name"] != "agent-weather" {
			t.Errorf("expected targetRef name 'agent-weather', got %v", targetRef["name"])
		}
	})
}

func TestCommonLabels(t *testing.T) {
	labels := commonLabels("my-agent")

	if labels[labelManagedBy] != managedByValue {
		t.Errorf("expected %s=%s", labelManagedBy, managedByValue)
	}
	if labels[labelAgentCard] != "my-agent" {
		t.Errorf("expected %s=my-agent", labelAgentCard)
	}
	if len(labels) != 2 {
		t.Errorf("expected exactly 2 labels, got %d", len(labels))
	}
}

func TestContainsProtocol(t *testing.T) {
	protocols := []string{"a2a", "mcp", "http"}

	if !containsProtocol(protocols, "mcp") {
		t.Error("expected to find 'mcp'")
	}
	if containsProtocol(protocols, "grpc") {
		t.Error("did not expect to find 'grpc'")
	}
	if containsProtocol(nil, "mcp") {
		t.Error("expected false for nil slice")
	}
}

func TestLabelsMatchSelector(t *testing.T) {
	objectLabels := map[string]string{
		"tier":    "premium",
		"region":  "us-east",
		"version": "v1",
	}

	if !labelsMatchSelector(objectLabels, map[string]string{"tier": "premium"}) {
		t.Error("expected match for subset selector")
	}
	if !labelsMatchSelector(objectLabels, map[string]string{"tier": "premium", "region": "us-east"}) {
		t.Error("expected match for multi-label selector")
	}
	if labelsMatchSelector(objectLabels, map[string]string{"tier": "standard"}) {
		t.Error("expected no match for wrong value")
	}
	if labelsMatchSelector(objectLabels, map[string]string{"missing": "label"}) {
		t.Error("expected no match for missing label")
	}
	if !labelsMatchSelector(objectLabels, map[string]string{}) {
		t.Error("expected match for empty selector")
	}
}
