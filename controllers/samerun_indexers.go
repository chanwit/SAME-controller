package controllers

import (
	"context"
	"fmt"

	programv1alpha1 "controller.projectsame.io/api/v1alpha1"
	"github.com/fluxcd/pkg/runtime/dependency"
	sourcev1 "github.com/fluxcd/source-controller/api/v1beta1"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"
)

func (r *SameRunReconciler) indexBy(kind string) func(o client.Object) []string {
	return func(o client.Object) []string {
		sameRun, ok := o.(*programv1alpha1.SameRun)
		if !ok {
			panic(fmt.Sprintf("Expected a SameRun object, got %T", o))
		}

		if sameRun.Spec.SourceRef.Kind == kind {
			namespace := sameRun.GetNamespace()
			if sameRun.Spec.SourceRef.Namespace != "" {
				namespace = sameRun.Spec.SourceRef.Namespace
			}
			return []string{fmt.Sprintf("%s/%s", namespace, sameRun.Spec.SourceRef.Name)}
		}

		return nil
	}
}

func (r *SameRunReconciler) requestsForRevisionChangeWith(indexKey string) func(obj client.Object) []reconcile.Request {
	return func(obj client.Object) []reconcile.Request {
		repo, ok := obj.(interface {
			GetArtifact() *sourcev1.Artifact
		})
		if !ok {
			panic(fmt.Sprintf("Expected an artifact (GitRepository or BitBucket) but got a %T", obj))
		}
		// If we do not have an artifact, we have no requests to make
		if repo.GetArtifact() == nil {
			return nil
		}

		ctx := context.Background()
		var list programv1alpha1.SameRunList
		if err := r.List(ctx, &list, client.MatchingFields{
			indexKey: ObjectKey(obj).String(),
		}); err != nil {
			return nil
		}
		var dd []dependency.Dependent
		for _, d := range list.Items {
			// If the revision of the artifact equals to the last attempted revision,
			// we should not make a request for this Kustomization
			if repo.GetArtifact().Revision == d.Status.LastAttemptedRevision {
				continue
			}
			dd = append(dd, d)
		}
		sorted, err := dependency.Sort(dd)
		if err != nil {
			return nil
		}
		reqs := make([]reconcile.Request, len(sorted), len(sorted))
		for i := range sorted {
			reqs[i].NamespacedName.Name = sorted[i].Name
			reqs[i].NamespacedName.Namespace = sorted[i].Namespace
		}
		return reqs
	}
}
