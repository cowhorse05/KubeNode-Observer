package topology

import (
	"testing"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
)

func TestPodInfoUsesMirrorHashForStaticPodRuntimeIdentity(t *testing.T) {
	pod := &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{
			UID:         types.UID("api-pod-uid"),
			Annotations: map[string]string{"kubernetes.io/config.mirror": "static-config-hash"},
		},
		Status: corev1.PodStatus{Phase: corev1.PodRunning, QOSClass: corev1.PodQOSBurstable},
	}

	info := podInfo(pod)
	if info.UID != "api-pod-uid" || info.RuntimeUID != "static-config-hash" || !info.Static {
		t.Fatalf("unexpected pod info: %#v", info)
	}
}

func TestGuestProbeOptInRequiresExplicitAnnotationAndRetainsNetworkScope(t *testing.T) {
	pod := &corev1.Pod{ObjectMeta: metav1.ObjectMeta{Annotations: map[string]string{"observer.kubenode.io/guest-probe": "enabled"}}, Spec: corev1.PodSpec{HostNetwork: true}, Status: corev1.PodStatus{PodIP: "192.0.2.1"}}
	info := podInfo(pod)
	if !info.GuestProbe || !info.HostNetwork || info.IP != "192.0.2.1" {
		t.Fatal(info)
	}
	pod.Annotations["observer.kubenode.io/guest-probe"] = "true"
	if podInfo(pod).GuestProbe {
		t.Fatal("implicit opt-in accepted")
	}
}
