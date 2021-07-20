/*


Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0

Unless required by applicable law or agreed to in writing, software
distributed under the License is distributed on an "AS IS" BASIS,
WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
See the License for the specific language governing permissions and
limitations under the License.
*/

package v1alpha1

import (
	"github.com/fluxcd/pkg/apis/meta"
	"github.com/fluxcd/pkg/runtime/dependency"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"time"
)

// EDIT THIS FILE!  THIS IS SCAFFOLDING FOR YOU TO OWN!
// NOTE: json tags are required.  Any new fields you add must have json tags for the fields to be serialized.

// SameRunSpec defines the desired state of SameRun
type SameRunSpec struct {
	// DependsOn may contain a dependency.CrossNamespaceDependencyReference slice
	// with references to SameRun resources that must be ready before this
	// SameRun can be reconciled.
	// +optional
	DependsOn []dependency.CrossNamespaceDependencyReference `json:"dependsOn,omitempty"`

	// The interval at which to reconcile the Kustomization.
	// +required
	Interval metav1.Duration `json:"interval"`

	// The interval at which to retry a previously failed reconciliation.
	// When not specified, the controller uses the KustomizationSpec.Interval
	// value to retry failures.
	// +optional
	RetryInterval *metav1.Duration `json:"retryInterval,omitempty"`

	// Path to the directory containing the kustomization.yaml file, or the
	// set of plain YAMLs a kustomization.yaml should be generated for.
	// Defaults to 'None', which translates to the root path of the SourceRef.
	// +optional
	Path string `json:"path,omitempty"`

	// Prune enables garbage collection.
	// +required
	Prune bool `json:"prune"`

	// Reference of the source where the kustomization file is.
	// +required
	SourceRef CrossNamespaceSourceReference `json:"sourceRef"`

	// This flag tells the controller to suspend subsequent kustomize executions,
	// it does not apply to already started executions. Defaults to false.
	// +optional
	Suspend bool `json:"suspend,omitempty"`

	// Timeout for validation, apply and health checking operations.
	// Defaults to 'Interval' duration.
	// +optional
	Timeout *metav1.Duration `json:"timeout,omitempty"`

	// // Preconditions is a CEL expression which must be satisfied in order to run the program
	// // +optional
	// Preconditions string `json:"preconditions,omitempty"`
}

// SameRunStatus defines the observed state of SameRun
type SameRunStatus struct {
	// ObservedGeneration is the last reconciled generation.
	// +optional
	ObservedGeneration int64 `json:"observedGeneration,omitempty"`

	// +optional
	Conditions []metav1.Condition `json:"conditions,omitempty"`

	// The last successfully applied revision.
	// The revision format for Git sources is <branch|tag>/<commit-sha>.
	// +optional
	LastAppliedRevision string `json:"lastAppliedRevision,omitempty"`

	// LastAttemptedRevision is the revision of the last reconciliation attempt.
	// +optional
	LastAttemptedRevision string `json:"lastAttemptedRevision,omitempty"`

	meta.ReconcileRequestStatus `json:",inline"`
}

// +genclient
// +genclient:Namespaced
// +kubebuilder:object:root=true
// +kubebuilder:resource:shortName=sr
// +kubebuilder:subresource:status
// +kubebuilder:printcolumn:name="Ready",type="string",JSONPath=".status.conditions[?(@.type==\"Ready\")].status",description=""
// +kubebuilder:printcolumn:name="Status",type="string",JSONPath=".status.conditions[?(@.type==\"Ready\")].message",description=""
// +kubebuilder:printcolumn:name="Age",type="date",JSONPath=".metadata.creationTimestamp",description=""

// SameRun is the Schema for the sameruns API
type SameRun struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`

	Spec   SameRunSpec   `json:"spec,omitempty"`
	Status SameRunStatus `json:"status,omitempty"`
}

// +kubebuilder:object:root=true

// SameRunList contains a list of SameRun
type SameRunList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitempty"`
	Items           []SameRun `json:"items"`
}

const (
	// GitRepositoryIndexKey is the key used for indexing sameRuns
	// based on their Git sources.
	GitRepositoryIndexKey string = ".metadata.gitRepository"

	// BucketIndexKey is the key used for indexing sameRuns
	// based on their S3 sources.
	BucketIndexKey string = ".metadata.bucket"

	SameRunFinalizer string = "finalizers.samerun"

	MaxConditionMessageLength = 20000
)

func SetSameRunReadiness(s *SameRun, status metav1.ConditionStatus, reason, message string, revision string) {
	meta.SetResourceCondition(s, meta.ReadyCondition, status, reason, trimString(message, MaxConditionMessageLength))
	s.Status.ObservedGeneration = s.Generation
	s.Status.LastAttemptedRevision = revision
}

// SameRunNotReady registers a failed apply attempt of the given Kustomization.
func SameRunNotReady(k SameRun, revision, reason, message string) SameRun {
	SetSameRunReadiness(&k, metav1.ConditionFalse, reason, trimString(message, MaxConditionMessageLength), revision)
	if revision != "" {
		k.Status.LastAttemptedRevision = revision
	}
	return k
}

func (in SameRun) GetDependsOn() (types.NamespacedName, []dependency.CrossNamespaceDependencyReference) {
	return types.NamespacedName{
		Namespace: in.Namespace,
		Name:      in.Name,
	}, in.Spec.DependsOn
}

// GetStatusConditions returns a pointer to the Status.Conditions slice
func (in *SameRun) GetStatusConditions() *[]metav1.Condition {
	return &in.Status.Conditions
}

func (in SameRun) GetRetryInterval() time.Duration {
	if in.Spec.RetryInterval != nil {
		return in.Spec.RetryInterval.Duration
	}
	return in.Spec.Interval.Duration
}

// GetTimeout returns the timeout with default.
func (in SameRun) GetTimeout() time.Duration {
	duration := in.Spec.Interval.Duration
	if in.Spec.Timeout != nil {
		duration = in.Spec.Timeout.Duration
	}
	if duration < time.Minute {
		return time.Minute
	}
	return duration
}

func SameRunProgressing(sameRun SameRun) SameRun {
	meta.SetResourceCondition(&sameRun, meta.ReadyCondition, metav1.ConditionUnknown, meta.ProgressingReason, "reconciliation in progress")
	return sameRun
}

func SameRunReady(k SameRun, revision, reason, message string) SameRun {
	SetSameRunReadiness(&k, metav1.ConditionTrue, reason, trimString(message, MaxConditionMessageLength), revision)
	k.Status.LastAppliedRevision = revision
	return k
}

func init() {
	SchemeBuilder.Register(&SameRun{}, &SameRunList{})
}

func trimString(str string, limit int) string {
	result := str
	chars := 0
	for i := range str {
		if chars >= limit {
			result = str[:i] + "..."
			break
		}
		chars++
	}
	return result
}
