package k8s

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes/fake"
)

func TestCapabilitiesListsGetAndLogs(t *testing.T) {
	c := NewWithClient(fake.NewSimpleClientset(), "ctx", Scope{})
	caps := c.Capabilities()
	require.Len(t, caps, 2)
	require.Equal(t, "k8s.get", caps[0].ID)
	require.Equal(t, "k8s.logs", caps[1].ID)
	var schema map[string]any
	require.NoError(t, json.Unmarshal(caps[1].ArgsSchema, &schema))
	require.Equal(t, false, schema["additionalProperties"])
}

func TestQueryK8sGetStripsManagedFields(t *testing.T) {
	cs := fake.NewSimpleClientset(&corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{
			Name: "api-1", Namespace: "p",
			ManagedFields: []metav1.ManagedFieldsEntry{{Manager: "x"}},
		},
	})
	c := NewWithClient(cs, "ctx", Scope{})
	raw, err := c.Query(context.Background(), "k8s.get",
		json.RawMessage(`{"kind":"Pod","namespace":"p","name":"api-1"}`))
	require.NoError(t, err)
	require.NotContains(t, string(raw), "managedFields")
	require.Contains(t, string(raw), "api-1")
}

func TestQueryK8sGetUnknownKind(t *testing.T) {
	c := NewWithClient(fake.NewSimpleClientset(), "ctx", Scope{})
	_, err := c.Query(context.Background(), "k8s.get", json.RawMessage(`{"kind":"Secret","name":"s"}`))
	require.ErrorContains(t, err, "unsupported kind")
}

func TestQueryK8sLogsTruncatesAndTails(t *testing.T) {
	cs := fake.NewSimpleClientset(&corev1.Pod{ObjectMeta: metav1.ObjectMeta{Name: "p", Namespace: "n"}})
	// The fake clientset's Pods(ns).GetLogs(...).DoRaw(ctx) returns the literal
	// bytes "fake logs" with no error, which is enough to assert the JSON
	// envelope shape and the pod field. The tail/byte truncation logic is
	// exercised directly in TestTruncateLog.
	c := NewWithClient(cs, "ctx", Scope{})
	raw, err := c.Query(context.Background(), "k8s.logs",
		json.RawMessage(`{"namespace":"n","pod":"p","tailLines":10}`))
	require.NoError(t, err)
	var out map[string]any
	require.NoError(t, json.Unmarshal(raw, &out))
	require.Equal(t, "p", out["pod"])
	require.Contains(t, out, "lines")
}

func TestQueryUnknownCapability(t *testing.T) {
	c := NewWithClient(fake.NewSimpleClientset(), "ctx", Scope{})
	_, err := c.Query(context.Background(), "k8s.bogus", nil)
	require.ErrorContains(t, err, "unknown capability")
}

func TestTruncateLog(t *testing.T) {
	// 500 newline-terminated lines, tail to 10.
	big := strings.Repeat("line\n", 500)
	out, truncated := truncateLog(big, 10)
	require.True(t, truncated)
	require.True(t, strings.HasPrefix(out, "…(truncated)\n"))
	body := strings.TrimPrefix(out, "…(truncated)\n")
	require.Equal(t, 10, strings.Count(body, "line"))

	// Small input: unchanged, no truncation.
	small := "one\ntwo\nthree\n"
	out2, truncated2 := truncateLog(small, 10)
	require.False(t, truncated2)
	require.Equal(t, small, out2)

	// Byte cap fires even when the line count is under the tail.
	huge := strings.Repeat("x", 300*1024)
	out3, truncated3 := truncateLog(huge, 100)
	require.True(t, truncated3)
	require.True(t, strings.HasPrefix(out3, "…(truncated)\n"))
	require.LessOrEqual(t, len(out3), len("…(truncated)\n")+256*1024)
}
