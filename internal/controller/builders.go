package controller

import (
	"fmt"
	"strings"

	corev1 "k8s.io/api/core/v1"
	networkingv1 "k8s.io/api/networking/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/util/intstr"
	"sigs.k8s.io/yaml"

	gatewayv1 "sigs.k8s.io/gateway-api/apis/v1"

	v1alpha1 "github.com/agentoperations/agent-access-control/api/v1alpha1"
)

const (
	labelManagedBy = "kagenti.com/managed-by"
	labelAgentCard = "kagenti.com/agent-card"
	managedByValue = "agent-access-control"
)

// commonLabels returns the standard labels applied to all generated resources.
func commonLabels(cardName string) map[string]string {
	return map[string]string{
		labelManagedBy: managedByValue,
		labelAgentCard: cardName,
	}
}

// setOwnerRef sets an owner reference on the owned object pointing to the owner object.
func setOwnerRef(obj metav1.Object, owner metav1.Object, gvk schema.GroupVersionKind) {
	isController := true
	blockOwnerDeletion := true
	obj.SetOwnerReferences([]metav1.OwnerReference{
		{
			APIVersion:         gvk.GroupVersion().String(),
			Kind:               gvk.Kind,
			Name:               owner.GetName(),
			UID:                owner.GetUID(),
			Controller:         &isController,
			BlockOwnerDeletion: &blockOwnerDeletion,
		},
	})
}

// BuildHTTPRoute constructs a Gateway API HTTPRoute for a given AgentCard.
// The route matches requests with a PathPrefix of /agents/{card.Name} and
// forwards them to a backend Service named {card.Name}-svc on the configured port.
func BuildHTTPRoute(card *v1alpha1.AgentCard, gatewayName, gatewayNamespace string) *gatewayv1.HTTPRoute {
	port := gatewayv1.PortNumber(card.Spec.ServicePort)
	if port == 0 {
		port = 8080
	}

	pathPrefix := gatewayv1.PathMatchPathPrefix
	pathValue := "/agents/" + card.Name

	gwGroup := gatewayv1.Group("gateway.networking.k8s.io")
	gwKind := gatewayv1.Kind("Gateway")
	gwNs := gatewayv1.Namespace(gatewayNamespace)

	svcName := gatewayv1.ObjectName(card.Name + "-svc")

	route := &gatewayv1.HTTPRoute{
		TypeMeta: metav1.TypeMeta{
			APIVersion: "gateway.networking.k8s.io/v1",
			Kind:       "HTTPRoute",
		},
		ObjectMeta: metav1.ObjectMeta{
			Name:      "agent-" + card.Name,
			Namespace: card.Namespace,
			Labels:    commonLabels(card.Name),
		},
		Spec: gatewayv1.HTTPRouteSpec{
			CommonRouteSpec: gatewayv1.CommonRouteSpec{
				ParentRefs: []gatewayv1.ParentReference{
					{
						Group:     &gwGroup,
						Kind:      &gwKind,
						Namespace: &gwNs,
						Name:      gatewayv1.ObjectName(gatewayName),
					},
				},
			},
			Rules: []gatewayv1.HTTPRouteRule{
				{
					Matches: []gatewayv1.HTTPRouteMatch{
						{
							Path: &gatewayv1.HTTPPathMatch{
								Type:  &pathPrefix,
								Value: &pathValue,
							},
						},
					},
					BackendRefs: []gatewayv1.HTTPBackendRef{
						{
							BackendRef: gatewayv1.BackendRef{
								BackendObjectReference: gatewayv1.BackendObjectReference{
									Name: svcName,
									Port: &port,
								},
							},
						},
					},
				},
			},
		},
	}

	setOwnerRef(&route.ObjectMeta, &card.ObjectMeta, schema.GroupVersionKind{
		Group:   "kagenti.com",
		Version: "v1alpha1",
		Kind:    "AgentCard",
	})

	return route
}

// resolveServiceAccount expands a short ServiceAccount name to a fully qualified
// system:serviceaccount:{namespace}:{name} format. If the value already contains
// a slash (namespace/name), the namespace part is used. Otherwise the policy's
// namespace is assumed.
func resolveServiceAccount(name, policyNamespace string) string {
	if strings.Contains(name, "/") {
		parts := strings.SplitN(name, "/", 2)
		return fmt.Sprintf("system:serviceaccount:%s:%s", parts[0], parts[1])
	}
	return fmt.Sprintf("system:serviceaccount:%s:%s", policyNamespace, name)
}

// BuildAuthPolicy constructs a Kuadrant AuthPolicy (unstructured) for a given
// AgentPolicy and AgentCard. It targets the specified HTTPRoute and configures
// JWT authentication along with pattern-matching authorization based on
// allowed ServiceAccounts from the ingress policy.
func BuildAuthPolicy(policy *v1alpha1.AgentPolicy, card *v1alpha1.AgentCard, httpRouteName string) *unstructured.Unstructured {
	// Build authorization predicates from allowed agents (ServiceAccount references).
	var predicates []interface{}
	if policy.Spec.Ingress != nil {
		for _, agent := range policy.Spec.Ingress.AllowedAgents {
			predicates = append(predicates, map[string]interface{}{
				"selector": "auth.identity.sub",
				"operator": "eq",
				"value":    resolveServiceAccount(agent, policy.Namespace),
			})
		}
	}

	authPolicy := &unstructured.Unstructured{
		Object: map[string]interface{}{
			"apiVersion": "kuadrant.io/v1",
			"kind":       "AuthPolicy",
			"metadata": map[string]interface{}{
				"name":      "ap-" + card.Name,
				"namespace": policy.Namespace,
				"labels":    labelsToUnstructured(commonLabels(card.Name)),
			},
			"spec": map[string]interface{}{
				"targetRef": map[string]interface{}{
					"group": "gateway.networking.k8s.io",
					"kind":  "HTTPRoute",
					"name":  httpRouteName,
				},
				"rules": map[string]interface{}{
					"authentication": map[string]interface{}{
						"jwt-auth": map[string]interface{}{
							"jwt": map[string]interface{}{
								"issuerUrl": "https://issuer.example.com",
							},
						},
					},
					"authorization": map[string]interface{}{
						"agent-access": map[string]interface{}{
							"patternMatching": map[string]interface{}{
								"patterns": predicates,
							},
						},
					},
				},
			},
		},
	}

	setUnstructuredOwnerRef(authPolicy, &policy.ObjectMeta, schema.GroupVersionKind{
		Group:   "kagenti.com",
		Version: "v1alpha1",
		Kind:    "AgentPolicy",
	})

	return authPolicy
}

// BuildRateLimitPolicy constructs a Kuadrant RateLimitPolicy (unstructured) for
// a given AgentPolicy and AgentCard. It targets the specified HTTPRoute and
// configures rate limits based on the policy's RequestsPerMinute setting.
func BuildRateLimitPolicy(policy *v1alpha1.AgentPolicy, card *v1alpha1.AgentCard, httpRouteName string) *unstructured.Unstructured {
	rpm := 60
	if policy.Spec.RateLimit != nil {
		rpm = policy.Spec.RateLimit.RequestsPerMinute
	}

	rlp := &unstructured.Unstructured{
		Object: map[string]interface{}{
			"apiVersion": "kuadrant.io/v1",
			"kind":       "RateLimitPolicy",
			"metadata": map[string]interface{}{
				"name":      "rlp-" + card.Name,
				"namespace": policy.Namespace,
				"labels":    labelsToUnstructured(commonLabels(card.Name)),
			},
			"spec": map[string]interface{}{
				"targetRef": map[string]interface{}{
					"group": "gateway.networking.k8s.io",
					"kind":  "HTTPRoute",
					"name":  httpRouteName,
				},
				"limits": map[string]interface{}{
					"agent-rate-limit": map[string]interface{}{
						"rates": []interface{}{
							map[string]interface{}{
								"limit":    int64(rpm),
								"window":   "1m",
							},
						},
					},
				},
			},
		},
	}

	setUnstructuredOwnerRef(rlp, &policy.ObjectMeta, schema.GroupVersionKind{
		Group:   "kagenti.com",
		Version: "v1alpha1",
		Kind:    "AgentPolicy",
	})

	return rlp
}

// sidecarConfig is the internal structure serialized to YAML for the sidecar ConfigMap.
type sidecarConfig struct {
	Gateway       sidecarGateway       `json:"gateway"`
	AllowedAgents []string             `json:"allowedAgents"`
	External      sidecarExternal      `json:"external"`
}

type sidecarGateway struct {
	Host string `json:"host"`
	Mode string `json:"mode"`
}

type sidecarExternal struct {
	Rules       []sidecarExternalRule `json:"rules"`
	DefaultMode string               `json:"defaultMode"`
}

type sidecarExternalRule struct {
	Host         string   `json:"host"`
	Mode         string   `json:"mode"`
	VaultPath    string   `json:"vaultPath,omitempty"`
	Audience     string   `json:"audience,omitempty"`
	Scopes       []string `json:"scopes,omitempty"`
	Header       string   `json:"header,omitempty"`
	HeaderPrefix string   `json:"headerPrefix,omitempty"`
}

// BuildSidecarConfigMap constructs a ConfigMap containing the sidecar proxy
// configuration derived from the AgentPolicy and AgentCard. The configuration
// is YAML-serialized under the "config.yaml" key.
func BuildSidecarConfigMap(policy *v1alpha1.AgentPolicy, card *v1alpha1.AgentCard) (*corev1.ConfigMap, error) {
	cfg := sidecarConfig{
		Gateway: sidecarGateway{
			Host: fmt.Sprintf("agent-gateway.%s.svc.cluster.local", card.Namespace),
			Mode: "passthrough",
		},
		AllowedAgents: policy.Spec.Agents,
	}

	if policy.Spec.External != nil {
		cfg.External.DefaultMode = policy.Spec.External.DefaultMode
		for _, r := range policy.Spec.External.Rules {
			cfg.External.Rules = append(cfg.External.Rules, sidecarExternalRule{
				Host:         r.Host,
				Mode:         r.Mode,
				VaultPath:    r.VaultPath,
				Audience:     r.Audience,
				Scopes:       r.Scopes,
				Header:       r.Header,
				HeaderPrefix: r.HeaderPrefix,
			})
		}
	}

	data, err := yaml.Marshal(cfg)
	if err != nil {
		return nil, fmt.Errorf("failed to marshal sidecar config: %w", err)
	}

	cm := &corev1.ConfigMap{
		TypeMeta: metav1.TypeMeta{
			APIVersion: "v1",
			Kind:       "ConfigMap",
		},
		ObjectMeta: metav1.ObjectMeta{
			Name:      "sidecar-config-" + card.Name,
			Namespace: card.Namespace,
			Labels:    commonLabels(card.Name),
		},
		Data: map[string]string{
			"config.yaml": string(data),
		},
	}

	setOwnerRef(&cm.ObjectMeta, &policy.ObjectMeta, schema.GroupVersionKind{
		Group:   "kagenti.com",
		Version: "v1alpha1",
		Kind:    "AgentPolicy",
	})

	return cm, nil
}

// BuildMCPServerRegistration constructs an MCPServerRegistration (unstructured)
// for a given AgentCard. It targets the specified HTTPRoute and configures the
// MCP tool prefix and path.
func BuildMCPServerRegistration(card *v1alpha1.AgentCard, httpRouteName string) *unstructured.Unstructured {
	mcpReg := &unstructured.Unstructured{
		Object: map[string]interface{}{
			"apiVersion": "mcp.kagenti.com/v1alpha1",
			"kind":       "MCPServerRegistration",
			"metadata": map[string]interface{}{
				"name":      "mcp-" + card.Name,
				"namespace": card.Namespace,
				"labels":    labelsToUnstructured(commonLabels(card.Name)),
			},
			"spec": map[string]interface{}{
				"targetRef": map[string]interface{}{
					"group": "gateway.networking.k8s.io",
					"kind":  "HTTPRoute",
					"name":  httpRouteName,
				},
				"toolPrefix": card.Name + "_",
				"path":       "/mcp",
			},
		},
	}

	setUnstructuredOwnerRef(mcpReg, &card.ObjectMeta, schema.GroupVersionKind{
		Group:   "kagenti.com",
		Version: "v1alpha1",
		Kind:    "AgentCard",
	})

	return mcpReg
}

// setUnstructuredOwnerRef sets an owner reference on an unstructured object.
func setUnstructuredOwnerRef(obj *unstructured.Unstructured, owner metav1.Object, gvk schema.GroupVersionKind) {
	ownerRef := map[string]interface{}{
		"apiVersion":         gvk.GroupVersion().String(),
		"kind":               gvk.Kind,
		"name":               owner.GetName(),
		"uid":                string(owner.GetUID()),
		"controller":         true,
		"blockOwnerDeletion": true,
	}

	metadata, ok := obj.Object["metadata"].(map[string]interface{})
	if !ok {
		metadata = map[string]interface{}{}
		obj.Object["metadata"] = metadata
	}
	metadata["ownerReferences"] = []interface{}{ownerRef}
}

// labelsToUnstructured converts a string map to an unstructured-compatible map.
func labelsToUnstructured(labels map[string]string) map[string]interface{} {
	result := make(map[string]interface{}, len(labels))
	for k, v := range labels {
		result[k] = v
	}
	return result
}

// BuildNetworkPolicy constructs a Kubernetes NetworkPolicy for egress enforcement.
// When the external defaultMode is "deny", the NetworkPolicy denies all egress
// except DNS (port 53) and the cluster gateway. The sidecar proxy provides
// defense-in-depth at the application layer; this NetworkPolicy is the primary
// network-level enforcement.
func BuildNetworkPolicy(policy *v1alpha1.AgentPolicy, card *v1alpha1.AgentCard) *networkingv1.NetworkPolicy {
	dnsPort := intstr.FromInt32(53)
	protocolUDP := corev1.ProtocolUDP
	protocolTCP := corev1.ProtocolTCP

	// Allow DNS resolution (required for any outbound connectivity).
	dnsEgressRule := networkingv1.NetworkPolicyEgressRule{
		Ports: []networkingv1.NetworkPolicyPort{
			{Port: &dnsPort, Protocol: &protocolUDP},
			{Port: &dnsPort, Protocol: &protocolTCP},
		},
	}

	// Allow egress to the gateway service within the cluster (for agent-to-agent calls).
	gatewayEgressRule := networkingv1.NetworkPolicyEgressRule{
		To: []networkingv1.NetworkPolicyPeer{
			{
				// Allow traffic to any pod with the gateway label within the cluster.
				// The sidecar handles per-host credential injection and hostname-level routing.
				NamespaceSelector: &metav1.LabelSelector{},
			},
		},
	}

	np := &networkingv1.NetworkPolicy{
		TypeMeta: metav1.TypeMeta{
			APIVersion: "networking.k8s.io/v1",
			Kind:       "NetworkPolicy",
		},
		ObjectMeta: metav1.ObjectMeta{
			Name:      "egress-" + card.Name,
			Namespace: card.Namespace,
			Labels:    commonLabels(card.Name),
		},
		Spec: networkingv1.NetworkPolicySpec{
			// Select the agent's pods using the agent-card label.
			PodSelector: metav1.LabelSelector{
				MatchLabels: map[string]string{
					"kagenti.com/agent-card": card.Name,
				},
			},
			PolicyTypes: []networkingv1.PolicyType{
				networkingv1.PolicyTypeEgress,
			},
			Egress: []networkingv1.NetworkPolicyEgressRule{
				dnsEgressRule,
				gatewayEgressRule,
			},
		},
	}

	setOwnerRef(&np.ObjectMeta, &policy.ObjectMeta, schema.GroupVersionKind{
		Group:   "kagenti.com",
		Version: "v1alpha1",
		Kind:    "AgentPolicy",
	})

	return np
}
