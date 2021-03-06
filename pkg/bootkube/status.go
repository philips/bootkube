package bootkube

import (
	"fmt"
	"path"
	"reflect"
	"strings"
	"time"

	"github.com/golang/glog"
	"github.com/kubernetes-incubator/bootkube/pkg/util/etcdutil"
	"k8s.io/kubernetes/pkg/api"
	"k8s.io/kubernetes/pkg/api/v1"
	"k8s.io/kubernetes/pkg/client/cache"
	clientset "k8s.io/kubernetes/pkg/client/clientset_generated/release_1_4"
	"k8s.io/kubernetes/pkg/client/restclient"
	"k8s.io/kubernetes/pkg/labels"
	"k8s.io/kubernetes/pkg/runtime"
	"k8s.io/kubernetes/pkg/util/wait"
	"k8s.io/kubernetes/pkg/watch"
)

const (
	appKey       = "k8s-app"
	doesNotExist = "DoesNotExist"
)

func WaitUntilPodsRunning(pods []string, timeout time.Duration, selfHostedEtcd bool) error {
	sc, err := NewStatusController(pods)
	if err != nil {
		return err
	}
	sc.Run()

	if err := wait.Poll(5*time.Second, timeout, sc.AllRunning); err != nil {
		return fmt.Errorf("error while checking pod status: %v", err)
	}

	if selfHostedEtcd {
		if err := etcdutil.Migrate(); err != nil {
			return err
		}
	}

	UserOutput("All self-hosted control plane components successfully started\n")
	return nil
}

type statusController struct {
	client        clientset.Interface
	podStore      cache.Store
	watchPods     []string
	lastPodPhases map[string]v1.PodPhase
}

func NewStatusController(pods []string) (*statusController, error) {
	client, err := clientset.NewForConfig(&restclient.Config{Host: insecureAPIAddr})
	if err != nil {
		return nil, err
	}
	return &statusController{client: client, watchPods: pods}, nil
}

func (s *statusController) Run() {
	// TODO(aaron): statically define the selector so we can skip this
	ls, err := labels.Parse(appKey)
	if err != nil {
		panic(err)
	}

	options := api.ListOptions{LabelSelector: ls}
	podStore, podController := cache.NewInformer(
		&cache.ListWatch{
			ListFunc: func(lo api.ListOptions) (runtime.Object, error) {
				return s.client.Core().Pods(api.NamespaceSystem).List(options)
			},
			WatchFunc: func(lo api.ListOptions) (watch.Interface, error) {
				return s.client.Core().Pods(api.NamespaceSystem).Watch(options)
			},
		},
		&v1.Pod{},
		30*time.Minute,
		cache.ResourceEventHandlerFuncs{},
	)
	s.podStore = podStore
	go podController.Run(wait.NeverStop)
}

func (s *statusController) AllRunning() (bool, error) {
	ps, err := s.PodStatus()
	if err != nil {
		glog.Infof("Error retriving pod statuses: %v", err)
		return false, nil
	}

	if s.lastPodPhases == nil {
		s.lastPodPhases = ps
	}

	// use lastPodPhases to print only pods whose phase has changed
	changed := !reflect.DeepEqual(ps, s.lastPodPhases)
	s.lastPodPhases = ps

	running := true
	for p, s := range ps {
		if changed {
			UserOutput("\tPod Status:%24s\t%s\n", p, s)
		}
		if s != v1.PodRunning {
			running = false
		}
	}
	return running, nil
}

func (s *statusController) PodStatus() (map[string]v1.PodPhase, error) {
	status := make(map[string]v1.PodPhase)

	podNames := s.podStore.ListKeys()
	for _, pod := range s.watchPods {
		// Pod names are suffixed with random data. Match on prefix
		podName := pod
		for _, pn := range podNames {
			if strings.HasPrefix(pn, path.Join(api.NamespaceSystem, pod)) {
				podName = pn
			}
		}
		p, exists, err := s.podStore.GetByKey(podName)
		if err != nil {
			return nil, err
		}
		if !exists {
			status[pod] = doesNotExist
			continue
		}
		if p, ok := p.(*v1.Pod); ok {
			status[pod] = p.Status.Phase
		}
	}
	return status, nil
}
