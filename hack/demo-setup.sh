#!/bin/bash
# Demo setup: clean previous resources and prepare namespace
set -e

NAMESPACE="${1:-agent-demo}"
GATEWAY_NS="${2:-openshift-ingress}"

echo "=== Cleaning up previous demo resources ==="

# Remove finalizers first (controller may not be running to handle them)
for resource in agentpolicies agentcards; do
  for name in $(kubectl get "$resource" -n "$NAMESPACE" -o name 2>/dev/null); do
    kubectl patch "$name" -n "$NAMESPACE" --type=json -p='[{"op":"remove","path":"/metadata/finalizers"}]' 2>/dev/null || true
  done
done

# Delete policies first (they reference cards)
kubectl delete agentpolicies --all -n "$NAMESPACE" --ignore-not-found 2>/dev/null || true
kubectl delete agentcards --all -n "$NAMESPACE" --ignore-not-found 2>/dev/null || true

# Clean generated resources
kubectl delete httproutes -l kagenti.com/managed-by=agent-access-control -n "$NAMESPACE" --ignore-not-found 2>/dev/null || true
kubectl delete configmaps -l kagenti.com/managed-by=agent-access-control -n "$NAMESPACE" --ignore-not-found 2>/dev/null || true

# Delete dummy agent services
kubectl delete deploy weather-agent code-review-agent -n "$NAMESPACE" --ignore-not-found 2>/dev/null || true
kubectl delete svc weather-agent-svc code-review-agent-svc -n "$NAMESPACE" --ignore-not-found 2>/dev/null || true

# Ensure namespace exists
kubectl create namespace "$NAMESPACE" --dry-run=client -o yaml | kubectl apply -f - 2>/dev/null

# Install CRDs
kubectl apply -f config/crd/bases/

# Create dummy agent deployments and services
for AGENT in weather-agent code-review-agent; do
cat <<EOF | kubectl apply -f -
apiVersion: apps/v1
kind: Deployment
metadata:
  name: $AGENT
  namespace: $NAMESPACE
spec:
  replicas: 1
  selector:
    matchLabels:
      app: $AGENT
  template:
    metadata:
      labels:
        app: $AGENT
        kagenti.com/agent: "true"
    spec:
      containers:
      - name: agent
        image: registry.access.redhat.com/ubi9/httpd-24:latest
        ports:
        - containerPort: 8080
---
apiVersion: v1
kind: Service
metadata:
  name: ${AGENT}-svc
  namespace: $NAMESPACE
spec:
  selector:
    app: $AGENT
  ports:
  - port: 8080
    targetPort: 8080
EOF
done

echo "=== Setup complete ==="
