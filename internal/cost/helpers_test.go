package cost

import (
	"testing"

	"github.com/stretchr/testify/require"
	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
)

// Shared test helpers for the cost package (Tasks 8, 9, 10). Keep names stable.

func ptr[T any](v T) *T { return &v }

func mustSheet(t *testing.T) *Sheet {
	t.Helper()
	s, err := Load()
	require.NoError(t, err)
	return s
}

func node(name, instanceType string) corev1.Node {
	return corev1.Node{ObjectMeta: metav1.ObjectMeta{Name: name,
		Labels: map[string]string{"node.kubernetes.io/instance-type": instanceType}}}
}

func ctrlRef(kind, ns, name string) metav1.OwnerReference {
	return metav1.OwnerReference{Kind: kind, Name: name,
		UID: types.UID(kind + "/" + ns + "/" + name), Controller: ptr(true)}
}

func runningPod(ns, name, nodeName string, owner metav1.OwnerReference) corev1.Pod {
	return corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: ns, OwnerReferences: []metav1.OwnerReference{owner}},
		Spec:       corev1.PodSpec{NodeName: nodeName},
		Status:     corev1.PodStatus{Phase: corev1.PodRunning},
	}
}

// costReqTemplate builds a one-container pod template with cpu/mem requests.
func costReqTemplate(cpu, mem string) corev1.PodTemplateSpec {
	return corev1.PodTemplateSpec{Spec: corev1.PodSpec{Containers: []corev1.Container{{
		Name: "app",
		Resources: corev1.ResourceRequirements{Requests: corev1.ResourceList{
			corev1.ResourceCPU:    resource.MustParse(cpu),
			corev1.ResourceMemory: resource.MustParse(mem),
		}},
	}}}}
}

func deploy(ns, name string, replicas int32, cpu, mem string) appsv1.Deployment {
	return appsv1.Deployment{
		ObjectMeta: metav1.ObjectMeta{Namespace: ns, Name: name, UID: types.UID("Deployment/" + ns + "/" + name)},
		Spec: appsv1.DeploymentSpec{
			Replicas: &replicas,
			Template: costReqTemplate(cpu, mem),
		},
	}
}

func statefulSet(ns, name string, replicas int32, cpu, mem string) appsv1.StatefulSet {
	return appsv1.StatefulSet{
		ObjectMeta: metav1.ObjectMeta{Namespace: ns, Name: name, UID: types.UID("StatefulSet/" + ns + "/" + name)},
		Spec: appsv1.StatefulSetSpec{
			Replicas: &replicas,
			Template: costReqTemplate(cpu, mem),
		},
	}
}

func ds(ns, name, cpu, mem string) appsv1.DaemonSet {
	return appsv1.DaemonSet{
		ObjectMeta: metav1.ObjectMeta{Namespace: ns, Name: name, UID: types.UID("DaemonSet/" + ns + "/" + name)},
		Spec: appsv1.DaemonSetSpec{
			Template: costReqTemplate(cpu, mem),
		},
	}
}
