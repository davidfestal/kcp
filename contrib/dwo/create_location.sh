#!/bin/bash

if [ $# -ne 1 ]; then
    echo "create_location.sh <location name>"
    echo
    echo " creates the physical location (kind cluster)"
    exit 1
fi
LOCATION_NAME=$1

KCP_ROOT="$(dirname "${BASH_SOURCE}")/../.."
DWO_DEMO_ROOT="$(dirname "${BASH_SOURCE}")"

echo
echo "= Creating the ${LOCATION_NAME} location as a kind cluster"
echo "============================================================"

mkdir -p ${DWO_DEMO_ROOT}/locations
kind create cluster --name ${LOCATION_NAME} --config ${DWO_DEMO_ROOT}/location.config --kubeconfig ${DWO_DEMO_ROOT}/locations/${LOCATION_NAME}.kubeconfig

echo
echo "= Generating the ${LOCATION_NAME} cluster resource: ${LOCATION_NAME}.yaml"
echo "=   (based on the generated ${LOCATION_NAME}.kubeconfig)"
echo "==========================================================================="

sed -e 's/^/    /' ${DWO_DEMO_ROOT}/locations/${LOCATION_NAME}.kubeconfig | cat <<EOF > ${DWO_DEMO_ROOT}/locations/${LOCATION_NAME}.yaml
apiVersion: cluster.example.dev/v1alpha1
kind: Cluster
metadata:
  name: ${LOCATION_NAME}
spec:
  kubeconfig: | 
EOF
sed -e 's/^/    /' ${DWO_DEMO_ROOT}/locations/${LOCATION_NAME}.kubeconfig >> ${DWO_DEMO_ROOT}/locations/${LOCATION_NAME}.yaml

echo
echo "= Installing the NGINX ingress support in the ${LOCATION_NAME} kind cluster"
echo "============================================================================"

export KUBECONFIG=${DWO_DEMO_ROOT}/locations/${LOCATION_NAME}.kubeconfig
curl https://raw.githubusercontent.com/kubernetes/ingress-nginx/main/deploy/static/provider/kind/deploy.yaml | sed "s/--publish-status-address=localhost/--report-node-internal-ip-address/g" | kubectl --kubeconfig=${KUBECONFIG} apply -f -
sleep 3
kubectl --kubeconfig=${KUBECONFIG} wait --namespace ingress-nginx \
  --for=condition=ready pod \
  --selector=app.kubernetes.io/component=controller \
  --timeout=90s

echo
echo "= Location ${LOCATION_NAME} is up and ready"
echo "============================================"
