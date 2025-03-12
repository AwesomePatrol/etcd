// Copyright 2025 The etcd Authors
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

package coverage_test

import (
	"encoding/json"
	"os"
	"strings"
	"testing"
)

type Tag struct {
	Key   string `json:"key"`
	Type  string `json:"type"`
	Value any    `json:"value"`
}

type Log struct {
	Timestamp int64 `json:"timestamp"`
	Fields    []Tag `json:"fields"`
}

type Span struct {
	TraceID       string `json:"traceID"`
	SpanID        string `json:"spanID"`
	OperationName string `json:"operationName"`
	Tags          []Tag  `json:"tags"`
	ProcessID     string `json:"processID"`
	Logs          []Log  `json:"logs"`
	ServiceName   string
}

type Process struct {
	ServiceName string `json:"serviceName"`
}

type Trace struct {
	TraceID   string             `json:"traceID"`
	Spans     []Span             `json:"spans"`
	Processes map[string]Process `json:"processes"`
}

type Dump struct {
	Data []Trace `json:"data"`
}

func TestInterfaceUse(t *testing.T) {
	b, err := os.ReadFile("testdata/all_traces_v8.json")
	if err != nil {
		t.Fatalf("read test data: %v", err)
	}
	var dump Dump
	json.Unmarshal(b, &dump)
	t.Logf("First trace: %+v\n", dump.Data[0])

	m := make(map[string]map[string]Span)
	missingApiserverSide := 0
	for _, trace := range dump.Data {
		m[trace.TraceID] = make(map[string]Span)
		var associatedToEtcd, associatedToApiserver bool
		for _, span := range trace.Spans {
			span.ServiceName = trace.Processes[span.ProcessID].ServiceName
			m[span.TraceID][span.SpanID] = span
			if span.ServiceName == "etcd" {
				associatedToEtcd = true
			}
			if span.ServiceName == "apiserver" {
				associatedToApiserver = true
			}
		}
		if !associatedToEtcd {
			t.Fatalf("Trace in apiserver only: %+v", trace)
		}
		if !associatedToApiserver {
			delete(m, trace.TraceID)
			missingApiserverSide++
		}
	}
	if missingApiserverSide > 0 {
		t.Logf("skipped %d incomplete traces", missingApiserverSide)
	}
	t.Run("interface_bypass", func(t *testing.T) {
		var interfaceBypass []map[string]Span
		for _, trace := range m {
			if !throughInterface(trace) {
				interfaceBypass = append(interfaceBypass, trace)
			}
		}
		t.Logf("traces which did not go through the interface: %d / %d", len(interfaceBypass), len(m))

		result := map[string]int{}
		bypassByOperationName := make(map[string]int)
		for _, trace := range interfaceBypass {
			for _, span := range trace {
				if span.ServiceName != "etcd" {
					continue
				}
				if getKey(span.Logs) == "/registry/health" {
					result["healthcheck"]++
					break
				}
				if getCountOnly(span.Logs) == "true" {
					result["count"]++
					break
				}
				if getCompareKey(span.Logs) == "compact_rev_key" {
					result["compact_check"]++
					break
				}
				rb := getRangeBegin(span.Logs)
				if strings.Count(rb, "/") == 2 && getLimit(span.Logs) == "1" {
					result["consistent_read"]++
					break
				}
				if strings.Contains(rb, "event") {
					result["event"]++
					break
				}
				bypassByOperationName[span.OperationName]++
				if strings.HasPrefix(span.OperationName, "etcdserverpb.KV") && bypassByOperationName[span.OperationName] < 2 {
					t.Logf("%+v", span)
				}
			}
		}
		t.Logf("%+v", result)
		t.Logf("%+v", bypassByOperationName)
		knownBypass := map[string]bool{
			"etcdserverpb.Lease/LeaseGrant":   true,
			"etcdserverpb.Maintenance/Status": true,
			"etcdserverpb.Watch/Watch":        true,
		}
		for op := range bypassByOperationName {
			if !knownBypass[op] {
				t.Errorf("operation not found in the set of known interface bypasses: %s", op)
			}
		}
	})
	t.Run("revision use in the interface", func(t *testing.T) {
		for op, percentageWithRev := range map[string]float64{
			"Get kubernetesEtcd":              0,
			"OptimisticPut kubernetesEtcd":    0.5,
			"OptimisticDelete kubernetesEtcd": 1,
			"List kubernetesEtcd":             0,
		} {
			t.Run(op, func(t *testing.T) {
				ranges := make([]Span, 0)
				for _, trace := range m {
					for _, span := range trace {
						if span.OperationName == op {
							ranges = append(ranges, span)
							break
						}
					}
				}
				result := map[string]int{
					"0":       0,
					"set":     0,
					"missing": 0,
				}
				for _, span := range ranges {
					result[getRevisionFromTags(span.Tags)]++
				}
				t.Logf("%+v", result)
				if c := result["missing"]; c > 0 {
					t.Errorf("some traces are missing revision tag, count=%d", c)
				}
				total := float64(len(ranges))
				if share := float64(result["set"]) / total; share > percentageWithRev*1.2 || share < percentageWithRev*0.8 {
					t.Errorf("expected rev>0 range calls %.2f to be close [±20%%] to %.2f", share, percentageWithRev)
				}
			})
		}
	})
}

func throughInterface(trace map[string]Span) bool {
	for _, span := range trace {
		if strings.HasSuffix(span.OperationName, "kubernetesEtcd") {
			return true
		}
	}
	return false
}

func getRevisionFromTags(tags []Tag) string {
	for _, tag := range tags {
		if tag.Key == "rev" {
			if tag.Type != "int64" {
				continue
			}
			if tag.Value.(float64) == 0 {
				return "0"
			}
			if tag.Value.(float64) > 0 {
				return "set"
			}
		}
	}
	return "missing"
}

func getKey(logs []Log) string {
	return getFieldFromLogs(logs, "range_begin")
}

func getCompareKey(logs []Log) string {
	return getFieldFromLogs(logs, "compare_key")
}

func getRangeBegin(logs []Log) string {
	return getFieldFromLogs(logs, "range_begin")
}

func getCountOnly(logs []Log) string {
	return getFieldFromLogs(logs, "count_only")
}

func getLimit(logs []Log) string {
	return getFieldFromLogs(logs, "limit")
}

func getFieldFromLogs(logs []Log, key string) string {
	for _, log := range logs {
		for _, field := range log.Fields {
			if field.Key != key {
				continue
			}
			return field.Value.(string)
		}
	}
	return ""
}
