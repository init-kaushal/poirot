package promql

import (
	"encoding/json"
	"fmt"
	"strconv"

	"github.com/init-kaushal/poirot/internal/snapshot"
)

type apiError struct {
	Type    string
	Message string
}

func (e *apiError) Error() string {
	return fmt.Sprintf("promql %s: %s", e.Type, e.Message)
}

type envelope struct {
	Status    string          `json:"status"`
	ErrorType string          `json:"errorType"`
	Error     string          `json:"error"`
	Data      json.RawMessage `json:"data"`
}

type vectorData struct {
	ResultType string `json:"resultType"`
	Result     []struct {
		Metric map[string]string `json:"metric"`
		Value  [2]any            `json:"value"`  // [ <ts float>, "<val string>" ]
		Values [][2]any          `json:"values"` // matrix rows
	} `json:"result"`
}

func decode(raw []byte) (*vectorData, error) {
	var env envelope
	if err := json.Unmarshal(raw, &env); err != nil {
		return nil, fmt.Errorf("promql: decode envelope: %w", err)
	}
	if env.Status == "error" {
		return nil, &apiError{Type: env.ErrorType, Message: env.Error}
	}
	if env.Status != "success" {
		return nil, fmt.Errorf("promql: unexpected status %q", env.Status)
	}
	var d vectorData
	if len(env.Data) > 0 {
		if err := json.Unmarshal(env.Data, &d); err != nil {
			return nil, fmt.Errorf("promql: decode data: %w", err)
		}
	}
	return &d, nil
}

func sampleValue(pair [2]any) (float64, error) {
	s, ok := pair[1].(string)
	if !ok {
		return 0, fmt.Errorf("promql: sample value is %T, want string", pair[1])
	}
	return strconv.ParseFloat(s, 64)
}

func parseInstant(raw []byte) ([]snapshot.MetricSample, error) {
	d, err := decode(raw)
	if err != nil {
		return nil, err
	}
	switch d.ResultType {
	case "", "vector", "scalar":
	default:
		return nil, fmt.Errorf("promql: instant query returned resultType %q", d.ResultType)
	}
	out := make([]snapshot.MetricSample, 0, len(d.Result))
	for _, r := range d.Result {
		v, err := sampleValue(r.Value)
		if err != nil {
			return nil, err
		}
		out = append(out, snapshot.MetricSample{Labels: r.Metric, Value: v})
	}
	return out, nil
}

func parseRange(raw []byte) ([]snapshot.MetricSample, error) {
	d, err := decode(raw)
	if err != nil {
		return nil, err
	}
	if d.ResultType != "" && d.ResultType != "matrix" {
		return nil, fmt.Errorf("promql: range query returned resultType %q", d.ResultType)
	}
	out := make([]snapshot.MetricSample, 0, len(d.Result))
	for _, r := range d.Result {
		if len(r.Values) == 0 {
			continue
		}
		v, err := sampleValue(r.Values[len(r.Values)-1])
		if err != nil {
			return nil, err
		}
		out = append(out, snapshot.MetricSample{Labels: r.Metric, Value: v})
	}
	return out, nil
}
