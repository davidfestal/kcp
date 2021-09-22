#!/bin/bash

KCP_ROOT="$(dirname "${BASH_SOURCE}")/../.."
DWO_DEMO_ROOT="$(dirname "${BASH_SOURCE}")"
LOCATION_NAME=${1:-west}

kind create cluster --name ${LOCATION_NAME} --config ${DWO_DEMO_ROOT}/location.config --kubeconfig ${DWO_DEMO_ROOT}/${LOCATION_NAME}.kubeconfig

sed -e 's/^/    /' ${DWO_DEMO_ROOT}/${LOCATION_NAME}.kubeconfig | cat <<EOF > ${DWO_DEMO_ROOT}/${LOCATION_NAME}.yaml
apiVersion: cluster.example.dev/v1alpha1
kind: Cluster
metadata:
  name: ${LOCATION_NAME}
spec:
  kubeconfig: | 
EOF
sed -e 's/^/    /' ${DWO_DEMO_ROOT}/${LOCATION_NAME}.kubeconfig >> ${DWO_DEMO_ROOT}/${LOCATION_NAME}.yaml

export KUBECONFIG=${DWO_DEMO_ROOT}/${LOCATION_NAME}.kubeconfig
kubectl --kubeconfig=${KUBECONFIG} apply -f https://raw.githubusercontent.com/kubernetes/ingress-nginx/main/deploy/static/provider/kind/deploy.yaml
sleep 1
kubectl --kubeconfig=${KUBECONFIG} wait --namespace ingress-nginx \
  --for=condition=ready pod \
  --selector=app.kubernetes.io/component=controller \
  --timeout=90s
