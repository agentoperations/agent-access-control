package controller

import (
	"context"
	"fmt"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"
	"sigs.k8s.io/controller-runtime/pkg/log"

	gatewayv1 "sigs.k8s.io/gateway-api/apis/v1"

	v1alpha1 "github.com/agentoperations/agent-access-control/api/v1alpha1"
)

const (
	agentCardFinalizer = "kagenti.com/agentcard-finalizer"
)

// AgentCardReconciler reconciles AgentCard objects.
type AgentCardReconciler struct {
	client.Client
	Scheme           *runtime.Scheme
	GatewayName      string
	GatewayNamespace string
}

// +kubebuilder:rbac:groups=kagenti.com,resources=agentcards,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups=kagenti.com,resources=agentcards/status,verbs=get;update;patch
// +kubebuilder:rbac:groups=kagenti.com,resources=agentcards/finalizers,verbs=update
// +kubebuilder:rbac:groups=gateway.networking.k8s.io,resources=httproutes,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups=mcp.kagenti.com,resources=mcpserverregistrations,verbs=get;list;watch;create;update;patch;delete

// Reconcile handles reconciliation of AgentCard resources.
func (r *AgentCardReconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	logger := log.FromContext(ctx)

	// Fetch the AgentCard instance.
	var card v1alpha1.AgentCard
	if err := r.Get(ctx, req.NamespacedName, &card); err != nil {
		if apierrors.IsNotFound(err) {
			logger.Info("AgentCard not found, likely deleted")
			return ctrl.Result{}, nil
		}
		return ctrl.Result{}, fmt.Errorf("failed to fetch AgentCard: %w", err)
	}

	// Handle deletion: remove finalizer.
	if !card.DeletionTimestamp.IsZero() {
		if controllerutil.ContainsFinalizer(&card, agentCardFinalizer) {
			controllerutil.RemoveFinalizer(&card, agentCardFinalizer)
			if err := r.Update(ctx, &card); err != nil {
				return ctrl.Result{}, fmt.Errorf("failed to remove finalizer: %w", err)
			}
		}
		return ctrl.Result{}, nil
	}

	// Add finalizer if not present.
	if !controllerutil.ContainsFinalizer(&card, agentCardFinalizer) {
		controllerutil.AddFinalizer(&card, agentCardFinalizer)
		if err := r.Update(ctx, &card); err != nil {
			return ctrl.Result{}, fmt.Errorf("failed to add finalizer: %w", err)
		}
	}

	// Build the HTTPRoute for this AgentCard.
	desired := BuildHTTPRoute(&card, r.GatewayName, r.GatewayNamespace)

	// Create or update the HTTPRoute.
	existing := &gatewayv1.HTTPRoute{}
	err := r.Get(ctx, types.NamespacedName{Name: desired.Name, Namespace: desired.Namespace}, existing)
	if err != nil {
		if apierrors.IsNotFound(err) {
			if err := r.Create(ctx, desired); err != nil {
				r.setReadyCondition(ctx, &card, metav1.ConditionFalse, "HTTPRouteCreateFailed", err.Error())
				return ctrl.Result{}, fmt.Errorf("failed to create HTTPRoute: %w", err)
			}
			logger.Info("Created HTTPRoute", "name", desired.Name)
		} else {
			return ctrl.Result{}, fmt.Errorf("failed to get HTTPRoute: %w", err)
		}
	} else {
		// Update existing HTTPRoute spec.
		existing.Spec = desired.Spec
		existing.Labels = desired.Labels
		existing.OwnerReferences = desired.OwnerReferences
		if err := r.Update(ctx, existing); err != nil {
			r.setReadyCondition(ctx, &card, metav1.ConditionFalse, "HTTPRouteUpdateFailed", err.Error())
			return ctrl.Result{}, fmt.Errorf("failed to update HTTPRoute: %w", err)
		}
		logger.Info("Updated HTTPRoute", "name", desired.Name)
	}

	// If "mcp" is in the card's protocols, build and create/update MCPServerRegistration.
	if containsProtocol(card.Spec.Protocols, "mcp") {
		mcpReg := BuildMCPServerRegistration(&card, desired.Name)
		if err := r.createOrUpdateUnstructured(ctx, mcpReg); err != nil {
			if !isCRDNotFound(err) {
				r.setReadyCondition(ctx, &card, metav1.ConditionFalse, "MCPRegistrationFailed", err.Error())
				return ctrl.Result{}, fmt.Errorf("failed to create/update MCPServerRegistration: %w", err)
			}
			logger.Info("MCPServerRegistration CRD not installed, skipping", "error", err.Error())
		} else {
			logger.Info("Created/updated MCPServerRegistration", "name", mcpReg.GetName())
		}
	}

	// Update status.
	card.Status.GeneratedHTTPRoute = desired.Name
	r.setReadyCondition(ctx, &card, metav1.ConditionTrue, "Reconciled", "AgentCard reconciled successfully")

	return ctrl.Result{}, nil
}

// setReadyCondition updates the Ready condition on the AgentCard status and persists it.
func (r *AgentCardReconciler) setReadyCondition(ctx context.Context, card *v1alpha1.AgentCard, status metav1.ConditionStatus, reason, message string) {
	logger := log.FromContext(ctx)

	meta.SetStatusCondition(&card.Status.Conditions, metav1.Condition{
		Type:               "Ready",
		Status:             status,
		Reason:             reason,
		Message:            message,
		LastTransitionTime: metav1.Now(),
	})

	if err := r.Status().Update(ctx, card); err != nil {
		logger.Error(err, "failed to update AgentCard status")
	}
}

// createOrUpdateUnstructured creates or updates an unstructured resource.
func (r *AgentCardReconciler) createOrUpdateUnstructured(ctx context.Context, desired *unstructured.Unstructured) error {
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

// containsProtocol checks if a slice of protocols contains the target protocol.
func containsProtocol(protocols []string, target string) bool {
	for _, p := range protocols {
		if p == target {
			return true
		}
	}
	return false
}

// isCRDNotFound checks if the error indicates that the CRD is not installed.
func isCRDNotFound(err error) bool {
	if err == nil {
		return false
	}
	// discovery errors and NoMatch errors indicate the CRD is not registered.
	if meta.IsNoMatchError(err) {
		return true
	}
	// Also check for "no matches for kind" in the error message as a fallback.
	return apierrors.IsNotFound(err)
}

// SetupWithManager sets up the controller with the Manager.
func (r *AgentCardReconciler) SetupWithManager(mgr ctrl.Manager) error {
	return ctrl.NewControllerManagedBy(mgr).
		For(&v1alpha1.AgentCard{}).
		Owns(&gatewayv1.HTTPRoute{}).
		Complete(r)
}

