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

package controllers

import (
	"context"
	"fmt"
	"io/ioutil"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	programv1alpha1 "controller.projectsame.io/api/v1alpha1"
	securejoin "github.com/cyphar/filepath-securejoin"
	"github.com/fluxcd/pkg/apis/meta"
	"github.com/fluxcd/pkg/runtime/events"
	"github.com/fluxcd/pkg/runtime/metrics"
	"github.com/fluxcd/pkg/runtime/predicates"
	"github.com/fluxcd/pkg/untar"
	sourcev1 "github.com/fluxcd/source-controller/api/v1beta1"
	"github.com/go-logr/logr"
	"github.com/hashicorp/go-retryablehttp"
	"github.com/pkg/errors"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	apimeta "k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	kuberecorder "k8s.io/client-go/tools/record"
	"k8s.io/client-go/tools/reference"
	"k8s.io/client-go/util/homedir"
	"sigs.k8s.io/cli-utils/pkg/kstatus/polling"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/builder"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"
	"sigs.k8s.io/controller-runtime/pkg/handler"
	"sigs.k8s.io/controller-runtime/pkg/predicate"
	"sigs.k8s.io/controller-runtime/pkg/source"
)

// SameRunReconciler reconciles a SameRun object
type SameRunReconciler struct {
	client.Client
	Log                   logr.Logger
	Scheme                *runtime.Scheme
	httpClient            *retryablehttp.Client
	EventRecorder         kuberecorder.EventRecorder
	ExternalEventRecorder *events.Recorder
	MetricsRecorder       *metrics.Recorder
	StatusPoller          *polling.StatusPoller
}

type SameRunReconcilerOptions struct {
	MaxConcurrentReconciles int
	HTTPRetry               int
}

// +kubebuilder:rbac:groups=program.projectsame.io,resources=sameruns,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups=program.projectsame.io,resources=sameruns/status,verbs=get;update;patch
// +kubebuilder:rbac:groups=program.projectsame.io,resources=sameruns/finalizers,verbs=get;create;update;patch;delete
// +kubebuilder:rbac:groups=source.toolkit.fluxcd.io,resources=buckets;gitrepositories,verbs=get;list;watch
// +kubebuilder:rbac:groups=source.toolkit.fluxcd.io,resources=buckets/status;gitrepositories/status,verbs=get
// +kubebuilder:rbac:groups="",resources=secrets;serviceaccounts,verbs=get;list;watch
// +kubebuilder:rbac:groups="",resources=events,verbs=create;patch

func (r *SameRunReconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	log := logr.FromContext(ctx)
	reconcileStart := time.Now()

	var sameRun programv1alpha1.SameRun
	if err := r.Get(ctx, req.NamespacedName, &sameRun); err != nil {
		return ctrl.Result{}, client.IgnoreNotFound(err)
	}

	// Record suspended status metric
	// TODO defer r.recordSuspension(ctx, sameRun)

	// Add our finalizer if it does not exist
	if !controllerutil.ContainsFinalizer(&sameRun, programv1alpha1.SameRunFinalizer) {
		controllerutil.AddFinalizer(&sameRun, programv1alpha1.SameRunFinalizer)
		if err := r.Update(ctx, &sameRun); err != nil {
			log.Error(err, "unable to register finalizer")
			return ctrl.Result{}, err
		}
	}

	if !sameRun.ObjectMeta.DeletionTimestamp.IsZero() {
		return r.reconcileDelete(ctx, sameRun)
	}

	// Return early if the Kustomization is suspended.
	if sameRun.Spec.Suspend {
		log.Info("Reconciliation is suspended for this object")
		return ctrl.Result{}, nil
	}

	// resolve source reference
	source, err := r.getSource(ctx, sameRun)
	if err != nil {
		if apierrors.IsNotFound(err) {
			msg := fmt.Sprintf("Source '%s' not found", sameRun.Spec.SourceRef.String())
			sameRun = programv1alpha1.SameRunNotReady(sameRun, "", programv1alpha1.ArtifactFailedReason, msg)
			if err := r.patchStatus(ctx, req, sameRun.Status); err != nil {
				log.Error(err, "unable to update status for source not found")
				return ctrl.Result{Requeue: true}, err
			}
			r.recordReadiness(ctx, sameRun)
			log.Info(msg)
			// do not requeue immediately, when the source is created the watcher should trigger a reconciliation
			return ctrl.Result{RequeueAfter: sameRun.GetRetryInterval()}, nil
		} else {
			// retry on transient errors
			return ctrl.Result{Requeue: true}, err
		}
	}

	if source.GetArtifact() == nil {
		msg := "Source is not ready, artifact not found"
		sameRun = programv1alpha1.SameRunNotReady(sameRun, "", programv1alpha1.ArtifactFailedReason, msg)
		if err := r.patchStatus(ctx, req, sameRun.Status); err != nil {
			log.Error(err, "unable to update status for artifact not found")
			return ctrl.Result{Requeue: true}, err
		}
		r.recordReadiness(ctx, sameRun)
		log.Info(msg)
		// do not requeue immediately, when the artifact is created the watcher should trigger a reconciliation
		return ctrl.Result{RequeueAfter: sameRun.GetRetryInterval()}, nil
	}

	if sameRun.Status.LastAppliedRevision == source.GetArtifact().Revision {
		return ctrl.Result{}, nil
	}

	// record reconciliation duration
	if r.MetricsRecorder != nil {
		objRef, err := reference.GetReference(r.Scheme, &sameRun)
		if err != nil {
			return ctrl.Result{}, err
		}
		defer r.MetricsRecorder.RecordDuration(*objRef, reconcileStart)
	}

	// set the reconciliation status to progressing
	sameRun = programv1alpha1.SameRunProgressing(sameRun)
	if err := r.patchStatus(ctx, req, sameRun.Status); err != nil {
		log.Error(err, "unable to update status to progressing")
		return ctrl.Result{Requeue: true}, err
	}
	r.recordReadiness(ctx, sameRun)

	// reconcile sameRun by applying the latest revision
	reconciledSameRun, reconcileErr := r.reconcile(ctx, *sameRun.DeepCopy(), source)
	if err := r.patchStatus(ctx, req, reconciledSameRun.Status); err != nil {
		log.Error(err, "unable to update status after reconciliation")
		return ctrl.Result{Requeue: true}, err
	}

	r.recordReadiness(ctx, reconciledSameRun)

	// broadcast the reconciliation failure and requeue at the specified retry interval
	if reconcileErr != nil {
		log.Error(reconcileErr, fmt.Sprintf("Reconciliation failed after %s, next try in %s",
			time.Now().Sub(reconcileStart).String(),
			sameRun.GetRetryInterval().String()),
			"revision",
			source.GetArtifact().Revision)
		r.event(ctx, reconciledSameRun, source.GetArtifact().Revision, events.EventSeverityError,
			reconcileErr.Error(), nil)
		return ctrl.Result{RequeueAfter: sameRun.GetRetryInterval()}, nil
	}

	// broadcast the reconciliation result and requeue at the specified interval
	log.Info(fmt.Sprintf("Reconciliation finished in %s, next run in %s",
		time.Now().Sub(reconcileStart).String(),
		sameRun.Spec.Interval.Duration.String()),
		"revision",
		source.GetArtifact().Revision,
	)

	r.event(ctx, reconciledSameRun, source.GetArtifact().Revision, events.EventSeverityInfo,
		"Update completed", map[string]string{"commit_status": "update"})

	return ctrl.Result{RequeueAfter: sameRun.Spec.Interval.Duration}, nil
}

func (r *SameRunReconciler) reconcile(_ context.Context, sameRun programv1alpha1.SameRun, source sourcev1.Source) (programv1alpha1.SameRun, error) {
	// record the value of the reconciliation request, if any
	if v, ok := meta.ReconcileAnnotationValue(sameRun.GetAnnotations()); ok {
		sameRun.Status.SetLastHandledReconcileRequest(v)
	}

	// create tmp dir
	tmpDir, err := ioutil.TempDir("", sameRun.Name)

	revision := source.GetArtifact().Revision

	if err != nil {
		err = fmt.Errorf("tmp dir error: %w", err)
		return programv1alpha1.SameRunNotReady(
			sameRun,
			revision,
			sourcev1.StorageOperationFailedReason,
			err.Error(),
		), err
	}
	defer os.RemoveAll(tmpDir)

	// download artifact and extract files
	err = r.download(source.GetArtifact().URL, tmpDir)
	if err != nil {
		return programv1alpha1.SameRunNotReady(
			sameRun,
			revision,
			programv1alpha1.ArtifactFailedReason,
			err.Error(),
		), err
	}

	// check build path exists
	dirPath, err := securejoin.SecureJoin(tmpDir, sameRun.Spec.Path)
	if err != nil {
		return programv1alpha1.SameRunNotReady(
			sameRun,
			revision,
			programv1alpha1.ArtifactFailedReason,
			err.Error(),
		), err
	}

	if _, err := os.Stat(dirPath); err != nil {
		err = fmt.Errorf("sameRun path not found: %w", err)
		return programv1alpha1.SameRunNotReady(
			sameRun,
			revision,
			programv1alpha1.ArtifactFailedReason,
			err.Error(),
		), err
	}

	if err := r.run(dirPath, sameRun, revision); err != nil {
		err = fmt.Errorf("sameRun failed: %w", err)
		return programv1alpha1.SameRunNotReady(
			sameRun,
			revision,
			programv1alpha1.ArtifactFailedReason,
			err.Error(),
		), err
	}

	return programv1alpha1.SameRunReady(
		sameRun,
		revision,
		meta.ReconciliationSucceededReason,
		"Applied revision: "+revision,
	), nil
}

func (r *SameRunReconciler) run(dirPath string, sameRun programv1alpha1.SameRun, revision string) error {
	// kubectl config set-cluster "${CLUSTER_NAME}" --server=https://kubernetes.default --certificate-authority=/var/run/secrets/kubernetes.io/serviceaccount/ca.crt
	if err := exec.Command("kubectl", "config",
		"set-cluster", "local",
		"--server=https://kubernetes.default", "--certificate-authority=/var/run/secrets/kubernetes.io/serviceaccount/ca.crt").Run(); err != nil {
		return errors.Wrap(err, "kubectl config set-cluster failed")
	}

	tokenBytes, err := ioutil.ReadFile("/var/run/secrets/kubernetes.io/serviceaccount/token")
	if err != nil {
		return errors.Wrap(err, "read token failed")
	}

	// kubectl config set-credentials pipeline --token "$(cat /var/run/secrets/kubernetes.io/serviceaccount/token)"
	if err := exec.Command("kubectl", "config",
		"set-credentials", "pipeline",
		"--token", string(tokenBytes)).Run(); err != nil {
		return errors.Wrap(err, "kubectl config set-credentials failed")
	}

	// kubectl config set-context kubeflow --cluster "${CLUSTER_NAME}" --user pipeline
	if err := exec.Command("kubectl", "config",
		"set-context", "kubeflow",
		"--cluster", "local",
		"--user", "pipeline").Run(); err != nil {
		return errors.Wrap(err, "kubectl config set-context kubeflow failed")
	}

	if err := exec.Command("kubectl", "config",
		"use-context", "kubeflow").Run(); err != nil {
		return errors.Wrap(err, "kubectl config use-context kubeflow failed")
	}

	sameConfigFilePath, err := securejoin.SecureJoin(dirPath, "same.yaml")
	if err != nil {
		return err
	}

	kubeconfigFile := filepath.Join(homedir.HomeDir(), ".kube", "config")

	hash := revision
	parts := strings.SplitN(revision, "/", 2)
	if len(parts) == 2 {
		hash = parts[1]
	}

	var cmd *exec.Cmd
	cmd = exec.Command("same", "program", "run",
		"--file", sameConfigFilePath,
		"--experiment-description", sameRun.Name,
		"--run-description", revision,
		"--run-param", "revision="+hash)
	cmd.Dir = dirPath
	//cmd.Stderr = os.Stderr
	//cmd.Stdout = os.Stdout
	//cmd.Stdin = os.Stdin
	cmd.Env = os.Environ()
	cmd.Env = append(cmd.Env, "KUBECONFIG="+kubeconfigFile)
	out, err := cmd.CombinedOutput()
	fmt.Print(string(out))
	if err != nil {
		return err
	}
	// workaround because CLI always return 0 (v0.0.56)
	if strings.Contains(string(out), "ERRO[") || strings.Contains(string(out), "Error:") {
		return errors.New("found error in the combined output")
	}

	return nil
}

func (r *SameRunReconciler) download(artifactURL string, tmpDir string) error {
	if hostname := os.Getenv("SOURCE_CONTROLLER_LOCALHOST"); hostname != "" {
		u, err := url.Parse(artifactURL)
		if err != nil {
			return err
		}
		u.Host = hostname
		artifactURL = u.String()
	}

	req, err := retryablehttp.NewRequest(http.MethodGet, artifactURL, nil)
	if err != nil {
		return fmt.Errorf("failed to create a new request: %w", err)
	}

	resp, err := r.httpClient.Do(req)
	if err != nil {
		return fmt.Errorf("failed to download artifact, error: %w", err)
	}
	defer resp.Body.Close()

	// check response
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("failed to download artifact from %s, status: %s", artifactURL, resp.Status)
	}

	// extract
	if _, err = untar.Untar(resp.Body, tmpDir); err != nil {
		return fmt.Errorf("failed to untar artifact, error: %w", err)
	}

	return nil
}

func (r *SameRunReconciler) SetupWithManager(mgr ctrl.Manager, opts SameRunReconcilerOptions) error {
	// Index the Kustomizations by the GitRepository references they (may) point at.
	if err := mgr.GetCache().IndexField(context.TODO(), &programv1alpha1.SameRun{}, programv1alpha1.GitRepositoryIndexKey,
		r.indexBy(sourcev1.GitRepositoryKind)); err != nil {
		return fmt.Errorf("failed setting index fields: %w", err)
	}

	// Index the Kustomizations by the Bucket references they (may) point at.
	if err := mgr.GetCache().IndexField(context.TODO(), &programv1alpha1.SameRun{}, programv1alpha1.BucketIndexKey,
		r.indexBy(sourcev1.BucketKind)); err != nil {
		return fmt.Errorf("failed setting index fields: %w", err)
	}

	// TODO r.requeueDependency = opts.DependencyRequeueInterval

	// Configure the retryable http client used for fetching artifacts.
	// By default it retries 10 times within a 3.5 minutes window.
	httpClient := retryablehttp.NewClient()
	httpClient.RetryWaitMin = 5 * time.Second
	httpClient.RetryWaitMax = 30 * time.Second
	httpClient.RetryMax = opts.HTTPRetry
	httpClient.Logger = nil
	r.httpClient = httpClient

	return ctrl.NewControllerManagedBy(mgr).
		For(&programv1alpha1.SameRun{}, builder.WithPredicates(
			predicate.Or(predicate.GenerationChangedPredicate{}, predicates.ReconcileRequestedPredicate{}),
		)).
		Watches(
			&source.Kind{Type: &sourcev1.GitRepository{}},
			handler.EnqueueRequestsFromMapFunc(r.requestsForRevisionChangeWith(programv1alpha1.GitRepositoryIndexKey)),
			builder.WithPredicates(SourceRevisionChangePredicate{}),
		).
		Watches(
			&source.Kind{Type: &sourcev1.Bucket{}},
			handler.EnqueueRequestsFromMapFunc(r.requestsForRevisionChangeWith(programv1alpha1.BucketIndexKey)),
			builder.WithPredicates(SourceRevisionChangePredicate{}),
		).
		WithOptions(controller.Options{MaxConcurrentReconciles: opts.MaxConcurrentReconciles}).
		Complete(r)
}

func (r *SameRunReconciler) recordReadiness(ctx context.Context, sameRun programv1alpha1.SameRun) {
	if r.MetricsRecorder == nil {
		return
	}
	log := logr.FromContext(ctx)

	objRef, err := reference.GetReference(r.Scheme, &sameRun)
	if err != nil {
		log.Error(err, "unable to record readiness metric")
		return
	}
	if rc := apimeta.FindStatusCondition(sameRun.Status.Conditions, meta.ReadyCondition); rc != nil {
		r.MetricsRecorder.RecordCondition(*objRef, *rc, !sameRun.DeletionTimestamp.IsZero())
	} else {
		r.MetricsRecorder.RecordCondition(*objRef, metav1.Condition{
			Type:   meta.ReadyCondition,
			Status: metav1.ConditionUnknown,
		}, !sameRun.DeletionTimestamp.IsZero())
	}
}

func (r *SameRunReconciler) getSource(ctx context.Context, sameRun programv1alpha1.SameRun) (sourcev1.Source, error) {
	var source sourcev1.Source
	sourceNamespace := sameRun.GetNamespace()
	if sameRun.Spec.SourceRef.Namespace != "" {
		sourceNamespace = sameRun.Spec.SourceRef.Namespace
	}
	namespacedName := types.NamespacedName{
		Namespace: sourceNamespace,
		Name:      sameRun.Spec.SourceRef.Name,
	}
	switch sameRun.Spec.SourceRef.Kind {
	case sourcev1.GitRepositoryKind:
		var repository sourcev1.GitRepository
		err := r.Client.Get(ctx, namespacedName, &repository)
		if err != nil {
			if apierrors.IsNotFound(err) {
				return source, err
			}
			return source, fmt.Errorf("unable to get source '%s': %w", namespacedName, err)
		}
		source = &repository
	case sourcev1.BucketKind:
		var bucket sourcev1.Bucket
		err := r.Client.Get(ctx, namespacedName, &bucket)
		if err != nil {
			if apierrors.IsNotFound(err) {
				return source, err
			}
			return source, fmt.Errorf("unable to get source '%s': %w", namespacedName, err)
		}
		source = &bucket
	default:
		return source, fmt.Errorf("source `%s` kind '%s' not supported",
			sameRun.Spec.SourceRef.Name, sameRun.Spec.SourceRef.Kind)
	}
	return source, nil
}

func (r *SameRunReconciler) patchStatus(ctx context.Context, req ctrl.Request, newStatus programv1alpha1.SameRunStatus) error {
	var sameRun programv1alpha1.SameRun
	if err := r.Get(ctx, req.NamespacedName, &sameRun); err != nil {
		return err
	}

	patch := client.MergeFrom(sameRun.DeepCopy())
	sameRun.Status = newStatus

	return r.Status().Patch(ctx, &sameRun, patch)
}

func (r *SameRunReconciler) event(ctx context.Context, sameRun programv1alpha1.SameRun, revision, severity, msg string, metadata map[string]string) {
	log := logr.FromContext(ctx)
	r.EventRecorder.Event(&sameRun, "Normal", severity, msg)
	objRef, err := reference.GetReference(r.Scheme, &sameRun)
	if err != nil {
		log.Error(err, "unable to send event")
		return
	}

	if r.ExternalEventRecorder != nil {
		if metadata == nil {
			metadata = map[string]string{}
		}
		if revision != "" {
			metadata["revision"] = revision
		}

		reason := severity
		if c := apimeta.FindStatusCondition(sameRun.Status.Conditions, meta.ReadyCondition); c != nil {
			reason = c.Reason
		}

		if err := r.ExternalEventRecorder.Eventf(*objRef, metadata, severity, reason, msg); err != nil {
			log.Error(err, "unable to send event")
			return
		}
	}
}

func (r *SameRunReconciler) reconcileDelete(ctx context.Context, sameRun programv1alpha1.SameRun) (ctrl.Result, error) {
	// Record deleted status
	r.recordReadiness(ctx, sameRun)

	// Remove our finalizer from the list and update it
	controllerutil.RemoveFinalizer(&sameRun, programv1alpha1.SameRunFinalizer)
	if err := r.Update(ctx, &sameRun); err != nil {
		return ctrl.Result{}, err
	}

	// Stop reconciliation as the object is being deleted
	return ctrl.Result{}, nil
}
