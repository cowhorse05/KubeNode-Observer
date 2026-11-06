package topology

import (
	"context"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/fields"
	"k8s.io/client-go/informers"
	"k8s.io/client-go/kubernetes"
	corelisters "k8s.io/client-go/listers/core/v1"
	"k8s.io/client-go/rest"
	"k8s.io/client-go/tools/cache"
)

type PodInfo struct {
	UID         string
	RuntimeUID  string
	Static      bool
	Phase       string
	QoSClass    string
	IP          string
	GuestProbe  bool
	HostNetwork bool
}

type NodeInfo struct {
	UID string
}

type ResourceResolver interface {
	Ready() bool
	Pod(namespace, name string) (PodInfo, bool)
	Node(name string) (NodeInfo, bool)
}

type KubernetesCache struct {
	podFactory  informers.SharedInformerFactory
	nodeFactory informers.SharedInformerFactory
	podLister   corelisters.PodLister
	nodeLister  corelisters.NodeLister
	podSynced   cache.InformerSynced
	nodeSynced  cache.InformerSynced
}

func NewKubernetesCache(nodeName string) (*KubernetesCache, error) {
	config, err := rest.InClusterConfig()
	if err != nil {
		return nil, err
	}
	client, err := kubernetes.NewForConfig(config)
	if err != nil {
		return nil, err
	}

	podFactory := informers.NewSharedInformerFactoryWithOptions(client, 0, informers.WithTweakListOptions(func(options *metav1.ListOptions) {
		options.FieldSelector = fields.OneTermEqualSelector("spec.nodeName", nodeName).String()
	}))
	nodeFactory := informers.NewSharedInformerFactoryWithOptions(client, 0, informers.WithTweakListOptions(func(options *metav1.ListOptions) {
		options.FieldSelector = fields.OneTermEqualSelector("metadata.name", nodeName).String()
	}))
	podInformer := podFactory.Core().V1().Pods()
	nodeInformer := nodeFactory.Core().V1().Nodes()
	return &KubernetesCache{
		podFactory:  podFactory,
		nodeFactory: nodeFactory,
		podLister:   podInformer.Lister(),
		nodeLister:  nodeInformer.Lister(),
		podSynced:   podInformer.Informer().HasSynced,
		nodeSynced:  nodeInformer.Informer().HasSynced,
	}, nil
}

func (c *KubernetesCache) Run(ctx context.Context) bool {
	c.podFactory.Start(ctx.Done())
	c.nodeFactory.Start(ctx.Done())
	return cache.WaitForCacheSync(ctx.Done(), c.podSynced, c.nodeSynced)
}

func (c *KubernetesCache) Ready() bool {
	return c.podSynced() && c.nodeSynced()
}

func (c *KubernetesCache) Pod(namespace, name string) (PodInfo, bool) {
	if !c.Ready() {
		return PodInfo{}, false
	}
	pod, err := c.podLister.Pods(namespace).Get(name)
	if err != nil {
		return PodInfo{}, false
	}
	return podInfo(pod), true
}

func podInfo(pod *corev1.Pod) PodInfo {
	runtimeUID := string(pod.UID)
	static := false
	if mirrorHash := pod.Annotations["kubernetes.io/config.mirror"]; mirrorHash != "" {
		runtimeUID = mirrorHash
		static = true
	}
	return PodInfo{
		UID:         string(pod.UID),
		RuntimeUID:  runtimeUID,
		Static:      static,
		Phase:       string(pod.Status.Phase),
		QoSClass:    string(pod.Status.QOSClass),
		IP:          pod.Status.PodIP,
		GuestProbe:  pod.Annotations["observer.kubenode.io/guest-probe"] == "enabled",
		HostNetwork: pod.Spec.HostNetwork,
	}
}

func (c *KubernetesCache) Node(name string) (NodeInfo, bool) {
	if !c.Ready() {
		return NodeInfo{}, false
	}
	node, err := c.nodeLister.Get(name)
	if err != nil {
		return NodeInfo{}, false
	}
	return NodeInfo{UID: string(node.UID)}, true
}
