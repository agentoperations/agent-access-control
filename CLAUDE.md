# Agent Access Control - Project Context

## What this project is

A Kubernetes operator that acts as an agent gateway. Two CRDs (AgentCard, AgentPolicy) generate six downstream resource types: HTTPRoute, AuthPolicy, RateLimitPolicy, NetworkPolicy, ConfigMap (sidecar), MCPServerRegistration.

Presentation: https://agentoperations.github.io/agent-access-control/presentation.html
Blog post: the entry `agent-access-control-zero-code-security.md` in the blog repo at `/Users/azaalouk/go/src/github.com/zanetworker/blog-concept`

## Key architectural decisions

### Personas are decoupled
- **AI Engineer**: builds agent, adds `kagenti.com/agent: true` label to Deployment, commits to git. Never writes CRDs, never runs kubectl, never touches security.
- **Platform Engineer**: writes AgentPolicy per tier, commits to platform repo, owns GitOps pipeline. Never touches agent code.
- **Discovery Controller** (not yet implemented): probes `/.well-known/agent.json` (A2A) or `tools/list` (MCP) on labeled pods and creates AgentCard CRs automatically.

### Egress enforcement is two layers
- **NetworkPolicy** (primary): kernel-level deny-all egress + allow DNS + allow gateway. Generated when `external.defaultMode: deny`. Pod cannot bypass it.
- **Sidecar** (defense-in-depth): per-host credential injection (vault, exchange) and hostname-level routing. NetworkPolicy only supports CIDR, sidecar handles hostnames.
- Both are needed. NetworkPolicy alone can't inject credentials. Sidecar alone can be bypassed.

### Identity is ServiceAccount-based
- `allowedAgents` references Kubernetes ServiceAccounts, not free-form strings.
- `resolveServiceAccount()` in `builders.go` expands `orchestrator` to `system:serviceaccount:{namespace}:orchestrator`.
- Supports `namespace/name` for cross-namespace.
- Maps directly to SPIFFE IDs if SPIRE/Istio adopted later -- zero migration.

### Meta-CRD tradeoff (Gordon's feedback)
- The value: 1 policy per tier vs N resources per agent. Automatic inheritance via labels. Consistency across a tier.
- The tradeoff: per-agent customization beyond what the CRD exposes requires dropping down to underlying resources directly.
- This tradeoff is documented honestly in the presentation ("Why a Meta-CRD?" slide) and the blog post.

## Current state (what's implemented vs designed)

### Implemented
- AgentCard + AgentPolicy CRDs with validation
- AgentCard controller: generates HTTPRoute, MCPServerRegistration (optional)
- AgentPolicy controller: generates AuthPolicy, RateLimitPolicy, NetworkPolicy, sidecar ConfigMap
- ServiceAccount-based identity in AuthPolicy predicates
- NetworkPolicy egress enforcement (deny-all + DNS + gateway)
- Graceful degradation when optional CRDs (AuthPolicy, RateLimitPolicy, MCPServerRegistration) are not installed
- Owner references for cascade deletion
- No-op update skipping to prevent reconcile thrashing

### Designed / not yet implemented
- Discovery controller (runtime agent probing from endpoints)
- Sidecar proxy containers (reverse proxy for inbound, forward proxy for outbound)
- Vault credential retrieval integration
- RFC 8693 Token Exchange
- Webhook admission validation
- Multi-cluster federation
- Observability / audit logging

## Lessons learned

### Reconcile thrashing
The controller `Owns(&corev1.ConfigMap{})`. Creating or updating a ConfigMap triggers another reconcile. If the reconcile always writes (even with identical content), it creates an infinite loop. Fixed by skipping updates when data hasn't changed (`mapsEqual` for ConfigMaps, existence check for NetworkPolicy and unstructured resources).

### Status update race condition
The ConfigMap ownership watch triggers a second reconcile that can overwrite the first reconcile's status update. Re-fetching the policy before status update (`r.Get()`) helps but doesn't fully resolve it -- it's a known cosmetic issue. The generated resources exist on the cluster; the status list may lag. A proper fix would use status patch or retry-on-conflict.

### NetworkPolicy limitations
Kubernetes NetworkPolicy only supports CIDR-based egress rules, not hostnames. You can't say "allow egress to api.github.com". The correct architecture is: NetworkPolicy for coarse deny-all + allow gateway/DNS, sidecar for fine-grained per-host logic. This was raised by Gordon Sim in review -- sidecar deny alone is insufficient because a process can bypass the sidecar with direct outbound connections.

### ServiceAccount vs free-form names
Gordon also flagged that using free-form agent names in `allowedAgents` is weak. ServiceAccounts provide cryptographic identity that Kubernetes already enforces. The `resolveServiceAccount()` function handles both short names (`orchestrator` = same namespace) and cross-namespace (`other-ns/planner`).

### Don't present designed features as implemented
The presentation originally mixed implemented and future features. After review, we split into a "Status" slide with "Implemented" vs "Designed/Future" columns. The blog post was also updated to be honest about what the sidecar and discovery controller don't do yet.

## File layout reference

```
api/v1alpha1/
  agentcard_types.go          # AgentCard CRD types
  agentpolicy_types.go        # AgentPolicy CRD types (allowedAgents = ServiceAccounts)

internal/controller/
  agentcard_controller.go     # Reconciles AgentCard -> HTTPRoute + MCPServerRegistration
  agentpolicy_controller.go   # Reconciles AgentPolicy -> AuthPolicy + RateLimitPolicy + NetworkPolicy + ConfigMap
  builders.go                 # BuildHTTPRoute, BuildAuthPolicy, BuildRateLimitPolicy, BuildNetworkPolicy, BuildSidecarConfigMap, BuildMCPServerRegistration, resolveServiceAccount
  builders_test.go            # Tests for all builders

config/crd/bases/             # Generated CRD YAML (make manifests)
config/samples/               # Example AgentCards and AgentPolicies
deploy/                       # Kubernetes deployment manifests (role.yaml has NetworkPolicy RBAC)
docs/
  presentation.html           # Reveal.js slide deck (served via GitHub Pages)
  auth-concepts.md            # Auth flows, JWT, token exchange, Vault
  demo-walkthrough.md         # Step-by-step demo with persona separation
  images/                     # 20 PNG diagrams
```

## Related projects

- **Agent Registry**: https://github.com/agentoperations/agent-registry -- build-time governance (LLM reads source code, generates BOM, promotion lifecycle). Complements this project: registry = "what does this agent depend on?", access control = "what can it do right now?"
- **MCP Gateway**: https://github.com/Kuadrant/mcp-gateway -- tool federation, per-agent tool filtering via MCPVirtualServer
