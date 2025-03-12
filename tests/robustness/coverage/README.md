## Overview

Go tests in this directory analyze the usage of Etcd API based on collected
traces from a Kubernetes cluster. They output information on:

1. Number of calls per gRPC method used by Kubernetes


### Manual test execution

At first we will manually set up the cluster, run e2e tests, download traces and
then execute the test.

1\. Set up [KIND
cluster](https://kind.sigs.k8s.io/docs/user/quick-start/#installation) with
tracing exporting to [Jaeger](https://www.jaegertracing.io/)

```
export KUBECONFIG="$(pwd)/kind-with-tracing-config"
kind create cluster --config kind-with-tracing.yaml
kubectl run jaeger --overrides='{ "apiVersion": "v1", "spec": { "hostNetwork": true, "nodeName": "kind-control-plane", "tolerations": [{"effect": "NoExecute", "operator": "Exists"}]} }' --labels='tier=control-plane' --image jaegertracing/jaeger:2.6.0
```

2\. Build and run Conformance tests from Kubernetes repository (this usually
takes 30-40m or will time out after 1 hour). It needs to be run from Kubernetes git repository:

```
export KUBECONFIG="$(pwd)/kind-with-tracing-config"
kind export kubeconfig
make WHAT="test/e2e/e2e.test"
./_output/bin/e2e.test -context kind-kind -ginkgo.focus=".*Conformance" -num-nodes 2
```

3\. Download traces and put them into `tests/robustness/coverage/testdata`
directory in Etcd git repository:

```
mkdir -p testdata
kubectl port-forward jaeger --address 0.0.0.0 --address :: 16686:16686 &
curl -v --retry 10 -H "Content-Type: application/json" -o testdata/demo_traces.json "http://127.0.0.1:16686/api/v3/traces?query.start_time_min=2025-07-01T10:00:00.00000000Z&query.start_time_max=2125-07-01T10:00:00.00000000Z&query.service_name=etcd"
```

4\. Run Go test

```
go test -v -timeout 60s go.etcd.io/etcd/tests/v3/robustness/coverage
```

### Automated test execution

In the future all steps above will be automated and executed periodically. This
is tracked in https://github.com/etcd-io/etcd/issues/20182
