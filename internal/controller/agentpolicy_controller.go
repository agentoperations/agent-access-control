package controller

import (
	"context"
	"fmt"

	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"
	"sigs.k8s.io/controller-runtime/pkg/handler"
	"sigs.k8s.io/controller-runtime/pkg/log"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"

	gatewayv1 "sigs.k8s.io/gateway-api/apis/v1"

	v1alpha1 "github.com/agentoperations/agent-access-control/api/v1alpha1"
)

const (
	agentPolicyFinalizer = "kagenti.com/agentpolicy-finalizer"
)

// AgentPolicyReconciler reconciles AgentPolicy objects.
type AgentPolicyReconciler struct {
	client.Client
	Scheme *runtime.Scheme
}

// +kubebuilder:rbac:groups=kagenti.com,resources=agentpolicies,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups=kagenti.com,resources=agentpolicies/status,verbs=get;update;patch
// +kubebuilder:rbac:groups=kagenti.com,resources=agentpolicies/finalizers,verbs=update
// +kubebuilder:rbac:groups=kagenti.com,resources=agentcards,verbs=get;list;watch
// +kubebuilder:rbac:groups=gateway.networking.k8s.io,resources=httproutes,verbs=get;list;watch
// +kubebuilder:rbac:groups=kuadrant.io,resources=authpolicies,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups=kuadrant.io,resources=ratelimitpolicies,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups="",resources=configmaps,verbs=get;list;watch;create;update;patch;delete

// Reconcile handles reconciliation of AgentPolicy resources.
func (r *AgentPolicyReconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	logger := log.FromContext(ctx)

	// Fetch the AgentPolicy instance.
	var policy v1alpha1.AgentPolicy
	if err := r.Get(ctx, req.NamespacedName, &policy); err != nil {
		if apierrors.IsNotFound(err) {
			logger.Info("AgentPolicy not found, likely deleted")
			return ctrl.Result{}, nil
		}
		return ctrl.Result{}, fmt.Errorf("failed to fetch AgentPolicy: %w", err)
	}

	// Handle deletion: remove finalizer.
	if !policy.DeletionTimestamp.IsZero() {
		if controllerutil.ContainsFinalizer(&policy, agentPolicyFinalizer) {
			controllerutil.RemoveFinalizer(&policy, agentPolicyFinalizer)
			if err := r.Update(ctx, &policy); err != nil {
				return ctrl.Result{}, fmt.Errorf("failed to remove finalizer: %w", err)
			}
		}
		return ctrl.Result{}, nil
	}

	// Add finalizer if not present.
	if !controllerutil.ContainsFinalizer(&policy, agentPolicyFinalizer) {
		controllerutil.AddFinalizer(&policy, agentPolicyFinalizer)
		if err := r.Update(ctx, &policy); err != nil {
			return ctrl.Result{}, fmt.Errorf("failed to add finalizer: %w", err)
		}
	}

	// List AgentCards matching the policy's selector.
	var cardList v1alpha1.AgentCardList
	if err := r.List(ctx, &cardList,
		client.InNamespace(req.Namespace),
		client.MatchingLabels(policy.Spec.AgentSelector.MatchLabels),
	); err != nil {
		r.setReadyCondition(ctx, &policy, metav1.ConditionFalse, "ListCardsFailed", err.Error())
		return ctrl.Result{}, fmt.Errorf("failed to list AgentCards: %w", err)
	}

	var generatedResources []v1alpha1.GeneratedResourceRef
	var reconcileErrors []error

	for i := range cardList.Items {
		card := &cardList.Items[i]

		// Find the HTTPRoute for this card by listing HTTPRoutes with the agent-card label.
		var routeList gatewayv1.HTTPRouteList
		if err := r.List(ctx, &routeList,
			client.InNamespace(card.Namespace),
			client.MatchingLabels{labelAgentCard: card.Name},
		); err != nil {
			reconcileErrors = append(reconcileErrors, fmt.Errorf("failed to list HTTPRoutes for card %s: %w", card.Name, err))
			continue
		}

		if len(routeList.Items) == 0 {
			logger.Info("No HTTPRoute found for AgentCard, skipping", "card", card.Name)
			continue
		}

		httpRouteName := routeList.Items[0].Name

		// Create AuthPolicy if ingress policy is defined.
		if policy.Spec.Ingress != nil {
			authPolicy := BuildAuthPolicy(&policy, card, httpRouteName)
			if err := r.createOrUpdateUnstructured(ctx, authPolicy); err != nil {
				if isCRDNotFoundPolicy(err) {
					logger.Info("AuthPolicy CRD not installed, skipping", "error", err.Error())
				} else {
					reconcileErrors = append(reconcileErrors, fmt.Errorf("failed to create/update AuthPolicy for card %s: %w", card.Name, err))
					continue
				}
			} else {
				generatedResources = append(generatedResources, v1alpha1.GeneratedResourceRef{
					Kind: "AuthPolicy",
					Name: authPolicy.GetName(),
				})
			}
		}

		// Create RateLimitPolicy if rate limit is defined.
		if policy.Spec.RateLimit != nil {
			rlp := BuildRateLimitPolicy(&policy, card, httpRouteName)
			if err := r.createOrUpdateUnstructured(ctx, rlp); err != nil {
				if isCRDNotFoundPolicy(err) {
					logger.Info("RateLimitPolicy CRD not installed, skipping", "error", err.Error())
				} else {
					reconcileErrors = append(reconcileErrors, fmt.Errorf("failed to create/update RateLimitPolicy for card %s: %w", card.Name, err))
					continue
				}
			} else {
				generatedResources = append(generatedResources, v1alpha1.GeneratedResourceRef{
					Kind: "RateLimitPolicy",
					Name: rlp.GetName(),
				})
			}
		}
	}

	// Create sidecar ConfigMaps if external policy is defined.
	if policy.Spec.External != nil {
		for i := range cardList.Items {
			card := &cardList.Items[i]

			cm, err := BuildSidecarConfigMap(&policy, card)
			if err != nil {
				reconcileErrors = append(reconcileErrors, fmt.Errorf("failed to build sidecar ConfigMap for card %s: %w", card.Name, err))
				continue
			}

			if err := r.createOrUpdateConfigMap(ctx, cm); err != nil {
				reconcileErrors = append(reconcileErrors, fmt.Errorf("failed to create/update sidecar ConfigMap for card %s: %w", card.Name, err))
				continue
			}

			generatedResources = append(generatedResources, v1alpha1.GeneratedResourceRef{
				Kind: "ConfigMap",
				Name: cm.Name,
			})
		}
	}

	// Update status.
	policy.Status.MatchedAgentCards = len(cardList.Items)
	policy.Status.GeneratedResources = generatedResources

	if len(reconcileErrors) > 0 {
		errMsg := fmt.Sprintf("encountered %d error(s) during reconciliation", len(reconcileErrors))
		for _, e := range reconcileErrors {
			errMsg += "; " + e.Error()
		}
		r.setReadyCondition(ctx, &policy, metav1.ConditionFalse, "ReconcileErrors", errMsg)
		return ctrl.Result{}, fmt.Errorf("reconciliation had errors: %v", reconcileErrors)
	}

	r.setReadyCondition(ctx, &policy, metav1.ConditionTrue, "Reconciled", "AgentPolicy reconciled successfully")

	return ctrl.Result{}, nil
}

// setReadyCondition updates the Ready condition on the AgentPolicy status and persists it.
func (r *AgentPolicyReconciler) setReadyCondition(ctx context.Context, policy *v1alpha1.AgentPolicy, status metav1.ConditionStatus, reason, message string) {
	logger := log.FromContext(ctx)

	meta.SetStatusCondition(&policy.Status.Conditions, metav1.Condition{
		Type:               "Ready",
		Status:             status,
		Reason:             reason,
		Message:            message,
		LastTransitionTime: metav1.Now(),
	})

	if err := r.Status().Update(ctx, policy); err != nil {
		logger.Error(err, "failed to update AgentPolicy status")
	}
}

// createOrUpdateUnstructured creates or updates an unstructured resource.
func (r *AgentPolicyReconciler) createOrUpdateUnstructured(ctx context.Context, desired *unstructured.Unstructured) error {
	existing := &unstructured.Unstructured{}
	existing.SetGroupVersionKind(desired.GroupVersionKind())

	err := r.Get(ctx, types.NamespacedName{
		Name:      desired.GetName(),
		Namespace: desired.GetNamespace(),
	}, existing)

	if err != nil {
		if apierrors.IsNotFound(err) {
			return r.Create(ctx, desired)
		}
		return err
	}

	// Preserve the resource version for update.
	desired.SetResourceVersion(existing.GetResourceVersion())
	return r.Update(ctx, desired)
}

// createOrUpdateConfigMap creates or updates a ConfigMap resource.
func (r *AgentPolicyReconciler) createOrUpdateConfigMap(ctx context.Context, desired *corev1.ConfigMap) error {
	existing := &corev1.ConfigMap{}
	err := r.Get(ctx, types.NamespacedName{
		Name:      desired.Name,
		Namespace: desired.Namespace,
	}, existing)

	if err != nil {
		if apierrors.IsNotFound(err) {
			return r.Create(ctx, desired)
		}
		return err
	}

	existing.Data = desired.Data
	existing.Labels = desired.Labels
	existing.OwnerReferences = desired.OwnerReferences
	return r.Update(ctx, existing)
}

// isCRDNotFoundPolicy checks if the error indicates that the CRD is not installed.
func isCRDNotFoundPolicy(err error) bool {
	if err == nil {
		return false
	}
	if meta.IsNoMatchError(err) {
		return true
	}
	return false
}

// findPoliciesForAgentCard maps an AgentCard to the AgentPolicies that select it.
func (r *AgentPolicyReconciler) findPoliciesForAgentCard(ctx context.Context, obj client.Object) []reconcile.Request {
	logger := log.FromContext(ctx)

	card, ok := obj.(*v1alpha1.AgentCard)
	if !ok {
		return nil
	}

	var policyList v1alpha1.AgentPolicyList
	if err := r.List(ctx, &policyList, client.InNamespace(card.Namespace)); err != nil {
		logger.Error(err, "failed to list AgentPolicies for mapping")
		return nil
	}

	var requests []reconcile.Request
	for _, policy := range policyList.Items {
		if labelsMatchSelector(card.Labels, policy.Spec.AgentSelector.MatchLabels) {
			requests = append(requests, reconcile.Request{
				NamespacedName: types.NamespacedName{
					Name:      policy.Name,
					Namespace: policy.Namespace,
				},
			})
		}
	}

	return requests
}

// labelsMatchSelector checks if all selector labels are present in the object's labels.
func labelsMatchSelector(objectLabels, selectorLabels map[string]string) bool {
	for key, val := range selectorLabels {
		if objectLabels[key] != val {
			return false
		}
	}
	return true
}

// SetupWithManager sets up the controller with the Manager.
func (r *AgentPolicyReconciler) SetupWithManager(mgr ctrl.Manager) error {
	return ctrl.NewControllerManagedBy(mgr).
		For(&v1alpha1.AgentPolicy{}).
		Owns(&corev1.ConfigMap{}).
		Watches(
			&v1alpha1.AgentCard{},
			handler.EnqueueRequestsFromMapFunc(r.findPoliciesForAgentCard),
		).
		Complete(r)
}
