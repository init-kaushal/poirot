package reliability

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	"github.com/init-kaushal/poirot/internal/analyzer"
	"github.com/init-kaushal/poirot/internal/snapshot"
)

func ruleIDs(fs []analyzer.Finding) []string {
	out := make([]string, len(fs))
	for i, f := range fs {
		out[i] = f.RuleID
	}
	return out
}

func TestCrashLoopIsCritical(t *testing.T) {
	snap := &snapshot.Snapshot{
		Meta: snapshot.Meta{CollectedAt: time.Now(), Lookback: 24 * time.Hour},
		Pods: []corev1.Pod{{
			ObjectMeta: metav1.ObjectMeta{Name: "api-1", Namespace: "payments"},
			Status: corev1.PodStatus{
				ContainerStatuses: []corev1.ContainerStatus{{
					Name:         "api",
					RestartCount: 7,
					State: corev1.ContainerState{
						Waiting: &corev1.ContainerStateWaiting{Reason: "CrashLoopBackOff"},
					},
				}},
			},
		}},
	}

	fs := checkCrashLoop(snap)
	require.Len(t, fs, 1)
	require.Equal(t, "reliability/crashloop", fs[0].RuleID)
	require.Equal(t, analyzer.SeverityCritical, fs[0].Severity)
	require.Equal(t, "Pod/payments/api-1", fs[0].Object.String())
	require.NotEmpty(t, fs[0].Evidence)
	require.Equal(t, "k8s", fs[0].Evidence[0].Source)
}

func TestPendingEscalatesWhenOld(t *testing.T) {
	now := time.Now()
	mk := func(age time.Duration) corev1.Pod {
		return corev1.Pod{
			ObjectMeta: metav1.ObjectMeta{
				Name: "p", Namespace: "d",
				CreationTimestamp: metav1.NewTime(now.Add(-age)),
			},
			Status: corev1.PodStatus{
				Phase: corev1.PodPending,
				Conditions: []corev1.PodCondition{{
					Type: corev1.PodScheduled, Status: corev1.ConditionFalse,
					Message: "0/3 nodes are available: insufficient cpu",
				}},
			},
		}
	}
	young := &snapshot.Snapshot{Meta: snapshot.Meta{CollectedAt: now}, Pods: []corev1.Pod{mk(2 * time.Minute)}}
	old := &snapshot.Snapshot{Meta: snapshot.Meta{CollectedAt: now}, Pods: []corev1.Pod{mk(30 * time.Minute)}}

	require.Equal(t, analyzer.SeverityWarning, checkPending(young)[0].Severity)
	require.Equal(t, analyzer.SeverityCritical, checkPending(old)[0].Severity)
}

func TestAnalyzeRunsPodRules(t *testing.T) {
	snap := &snapshot.Snapshot{
		Meta: snapshot.Meta{CollectedAt: time.Now(), Lookback: time.Hour},
		Pods: []corev1.Pod{{
			ObjectMeta: metav1.ObjectMeta{Name: "x", Namespace: "n"},
			Status: corev1.PodStatus{
				ContainerStatuses: []corev1.ContainerStatus{{
					Name: "c",
					LastTerminationState: corev1.ContainerState{
						Terminated: &corev1.ContainerStateTerminated{Reason: "OOMKilled", ExitCode: 137},
					},
				}},
			},
		}},
	}
	fs, err := New().Analyze(context.Background(), snap)
	require.NoError(t, err)
	require.Contains(t, ruleIDs(fs), "reliability/oomkilled")
}
