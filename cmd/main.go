package main

import (
	"flag"
	"os"

	"k8s.io/apimachinery/pkg/runtime"
	utilruntime "k8s.io/apimachinery/pkg/util/runtime"
	clientgoscheme "k8s.io/client-go/kubernetes/scheme"
	_ "k8s.io/client-go/plugin/pkg/client/auth"
	metricsclient "k8s.io/metrics/pkg/client/clientset/versioned"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/healthz"
	"sigs.k8s.io/controller-runtime/pkg/log/zap"
	metricsserver "sigs.k8s.io/controller-runtime/pkg/metrics/server"

	slumlordv1alpha1 "github.com/cschockaert/slumlord/api/v1alpha1"
	"github.com/cschockaert/slumlord/internal/controller"
	"github.com/cschockaert/slumlord/internal/dashboard"
)

var (
	scheme   = runtime.NewScheme()
	setupLog = ctrl.Log.WithName("setup")
)

func init() {
	utilruntime.Must(clientgoscheme.AddToScheme(scheme))
	utilruntime.Must(slumlordv1alpha1.AddToScheme(scheme))
}

func main() {
	var metricsAddr string
	var probeAddr string
	var dashboardAddr string
	var enableLeaderElection bool
	var enableIdleDetector bool
	var enableBinPacker bool
	var enableNodeDrain bool

	flag.StringVar(&metricsAddr, "metrics-bind-address", ":8080", "The address the metric endpoint binds to.")
	flag.StringVar(&probeAddr, "health-probe-bind-address", ":8081", "The address the probe endpoint binds to.")
	flag.StringVar(&dashboardAddr, "dashboard-bind-address", ":8082", "The address the dashboard binds to. Set to 0 to disable.")
	flag.BoolVar(&enableLeaderElection, "leader-elect", false,
		"Enable leader election for controller manager. "+
			"Enabling this will ensure there is only one active controller manager.")
	flag.BoolVar(&enableIdleDetector, "enable-idle-detector", false,
		"Enable the idle detector controller (experimental).")
	flag.BoolVar(&enableBinPacker, "enable-binpacker", false,
		"Enable the bin packer controller for node consolidation.")
	flag.BoolVar(&enableNodeDrain, "enable-node-drain", false,
		"Enable the node drain policy controller.")

	opts := zap.Options{Development: true}
	opts.BindFlags(flag.CommandLine)
	flag.Parse()

	ctrl.SetLogger(zap.New(zap.UseFlagOptions(&opts)))

	mgr, err := ctrl.NewManager(ctrl.GetConfigOrDie(), ctrl.Options{
		Scheme:                 scheme,
		Metrics:                metricsserver.Options{BindAddress: metricsAddr},
		HealthProbeBindAddress: probeAddr,
		LeaderElection:         enableLeaderElection,
		LeaderElectionID:       "slumlord.io",
	})
	if err != nil {
		setupLog.Error(err, "unable to start manager")
		os.Exit(1)
	}

	if err = (&controller.SleepScheduleReconciler{
		Client:   mgr.GetClient(),
		Scheme:   mgr.GetScheme(),
		Recorder: mgr.GetEventRecorder("slumlord-sleep-schedule"),
	}).SetupWithManager(mgr); err != nil {
		setupLog.Error(err, "unable to create controller", "controller", "SlumlordSleepSchedule")
		os.Exit(1)
	}

	if enableIdleDetector {
		// Create metrics clientset for querying metrics-server
		var mc metricsclient.Interface
		metricsClientset, metricsErr := metricsclient.NewForConfig(mgr.GetConfig())
		if metricsErr != nil {
			setupLog.Error(metricsErr, "failed to create metrics client, idle detector will run in degraded mode")
		} else {
			mc = metricsClientset
		}

		if err = (&controller.IdleDetectorReconciler{
			Client:        mgr.GetClient(),
			Scheme:        mgr.GetScheme(),
			MetricsClient: mc,
		}).SetupWithManager(mgr); err != nil {
			setupLog.Error(err, "unable to create controller", "controller", "SlumlordIdleDetector")
			os.Exit(1)
		}
	} else {
		setupLog.Info("idle detector controller disabled")
	}

	if enableBinPacker {
		if err = (&controller.BinPackerReconciler{
			Client: mgr.GetClient(),
			Scheme: mgr.GetScheme(),
		}).SetupWithManager(mgr); err != nil {
			setupLog.Error(err, "unable to create controller", "controller", "SlumlordBinPacker")
			os.Exit(1)
		}
	} else {
		setupLog.Info("bin packer controller disabled")
	}

	if enableNodeDrain {
		if err = (&controller.NodeDrainPolicyReconciler{
			Client:   mgr.GetClient(),
			Scheme:   mgr.GetScheme(),
			Recorder: mgr.GetEventRecorder("slumlord-node-drain"),
		}).SetupWithManager(mgr); err != nil {
			setupLog.Error(err, "unable to create controller", "controller", "SlumlordNodeDrainPolicy")
			os.Exit(1)
		}
	} else {
		setupLog.Info("node drain policy controller disabled")
	}

	if dashboardAddr != "0" {
		if err := mgr.Add(dashboard.NewServer(dashboardAddr, mgr.GetClient())); err != nil {
			setupLog.Error(err, "unable to set up dashboard")
			os.Exit(1)
		}
	} else {
		setupLog.Info("dashboard disabled")
	}

	if err := mgr.AddHealthzCheck("healthz", healthz.Ping); err != nil {
		setupLog.Error(err, "unable to set up health check")
		os.Exit(1)
	}
	if err := mgr.AddReadyzCheck("readyz", healthz.Ping); err != nil {
		setupLog.Error(err, "unable to set up ready check")
		os.Exit(1)
	}

	setupLog.Info("starting manager")
	if err := mgr.Start(ctrl.SetupSignalHandler()); err != nil {
		setupLog.Error(err, "problem running manager")
		os.Exit(1)
	}
}
