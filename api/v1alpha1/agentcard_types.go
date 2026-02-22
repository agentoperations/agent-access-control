package v1alpha1

import (
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

// AgentSkill describes a capability or skill that an agent provides.
type AgentSkill struct {
	// Name is the identifier for this skill.
	Name string `json:"name"`

	// Description provides a human-readable explanation of the skill.
	Description string `json:"description"`
}

// AgentCardSpec defines the desired state of AgentCard.
type AgentCardSpec struct {
	// Description is a human-readable description of the agent.
	Description string `json:"description"`

	// Skills lists the capabilities this agent provides.
	Skills []AgentSkill `json:"skills"`

	// Protocols lists the communication protocols the agent supports.
	// +kubebuilder:validation:MinItems=1
	Protocols []string `json:"protocols"`

	// ServicePort is the port on which the agent service listens.
	// +kubebuilder:default=8080
	ServicePort int32 `json:"servicePort"`
}

// AgentCardStatus defines the observed state of AgentCard.
type AgentCardStatus struct {
	// Conditions represent the latest available observations of the AgentCard's state.
	// +listType=map
	// +listMapKey=type
	Conditions []metav1.Condition `json:"conditions,omitempty"`

	// GeneratedHTTPRoute is the name of the HTTPRoute created for this AgentCard.
	GeneratedHTTPRoute string `json:"generatedHTTPRoute,omitempty"`
}

// +kubebuilder:object:root=true
// +kubebuilder:subresource:status
// +kubebuilder:resource:shortName=ac
// +kubebuilder:printcolumn:name="Description",type=string,JSONPath=`.spec.description`
// +kubebuilder:printcolumn:name="Protocols",type=string,JSONPath=`.spec.protocols`
// +kubebuilder:printcolumn:name="Ready",type=string,JSONPath=`.status.conditions[?(@.type=="Ready")].status`
// +kubebuilder:printcolumn:name="Age",type=date,JSONPath=`.metadata.creationTimestamp`

// AgentCard is the Schema for the agentcards API.
type AgentCard struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`

	Spec   AgentCardSpec   `json:"spec,omitempty"`
	Status AgentCardStatus `json:"status,omitempty"`
}

// +kubebuilder:object:root=true

// AgentCardList contains a list of AgentCard.
type AgentCardList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitempty"`
	Items           []AgentCard `json:"items"`
}

func init() {
	SchemeBuilder.Register(&AgentCard{}, &AgentCardList{})
}
