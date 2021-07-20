package v1alpha1

const (
	// HealthyCondition is the condition type used
	// to record the last health assessment result.
	HealthyCondition string = "Healthy"

	// PruneFailedReason represents the fact that the
	// pruning of the Kustomization failed.
	PruneFailedReason string = "PruneFailed"

	// ArtifactFailedReason represents the fact that the
	// artifact download of the kustomization failed.
	ArtifactFailedReason string = "ArtifactFailed"

	// BuildFailedReason represents the fact that the
	// kustomize build of the Kustomization failed.
	BuildFailedReason string = "BuildFailed"

	// HealthCheckFailedReason represents the fact that
	// one of the health checks of the Kustomization failed.
	HealthCheckFailedReason string = "HealthCheckFailed"

	// ValidationFailedReason represents the fact that the
	// validation of the Kustomization manifests has failed.
	ValidationFailedReason string = "ValidationFailed"
)
