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

package main

import (
	"flag"
	"os"

	sourcev1 "github.com/fluxcd/source-controller/api/v1beta1"
	"k8s.io/apimachinery/pkg/runtime"
	clientgoscheme "k8s.io/client-go/kubernetes/scheme"
	_ "k8s.io/client-go/plugin/pkg/client/auth/gcp"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/log/zap"

	programv1alpha1 "controller.projectsame.io/api/v1alpha1"
	"controller.projectsame.io/controllers"
	// +kubebuilder:scaffold:imports
)

var (
	scheme   = runtime.NewScheme()
	setupLog = ctrl.Log.WithName("setup")
)

func init() {
	_ = clientgoscheme.AddToScheme(scheme)

	_ = sourcev1.AddToScheme(scheme)
	_ = programv1alpha1.AddToScheme(scheme)
	// +kubebuilder:scaffold:scheme
}

var (
	metricsAddr          string
	enableLeaderElection bool
	concurrent           int
	watchAllNamespaces   bool
	httpRetry            int
)

const controllerName = "samerun-controller"

func main() {
	flag.StringVar(&metricsAddr, "metrics-addr", ":8080", "The address the metric endpoint binds to.")
	flag.BoolVar(&enableLeaderElection, "enable-leader-election", false,
		"Enable leader election for controller manager. "+
			"Enabling this will ensure there is only one active controller manager.")
	flag.IntVar(&concurrent, "concurrent", 1, "The number of concurrent kustomize reconciles.")
	flag.BoolVar(&watchAllNamespaces, "watch-all-namespaces", true, "Watch for custom resources in all namespaces, if set to false it will only watch the runtime namespace.")
	flag.IntVar(&httpRetry, "http-retry", 9, "The maximum number of retries when failing to fetch artifacts over HTTP.")

	flag.Parse()

	ctrl.SetLogger(zap.New(zap.UseDevMode(true)))

	mgr, err := ctrl.NewManager(ctrl.GetConfigOrDie(), ctrl.Options{
		Scheme:             scheme,
		MetricsBindAddress: metricsAddr,
		Port:               9443,
		LeaderElection:     enableLeaderElection,
		LeaderElectionID:   "3cf7dae2.projectsame.io",
	})
	if err != nil {
		setupLog.Error(err, "unable to start manager")
		os.Exit(1)
	}

	if err = (&controllers.SameRunReconciler{
		Client:        mgr.GetClient(),
		Log:           ctrl.Log.WithName("controllers").WithName("SameRun"),
		Scheme:        mgr.GetScheme(),
		EventRecorder: mgr.GetEventRecorderFor(controllerName),
	}).SetupWithManager(mgr, controllers.SameRunReconcilerOptions{
		MaxConcurrentReconciles: concurrent,
		HTTPRetry:               httpRetry,
	}); err != nil {
		setupLog.Error(err, "unable to create controller", "controller", "SameRun")
		os.Exit(1)
	}
	// +kubebuilder:scaffold:builder

	setupLog.Info("starting manager")
	if err := mgr.Start(ctrl.SetupSignalHandler()); err != nil {
		setupLog.Error(err, "problem running manager")
		os.Exit(1)
	}
}
