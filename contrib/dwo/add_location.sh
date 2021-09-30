#!/bin/bash

if [ $# -ne 2 ]; then
    echo "add_location.sh <location name> <logical cluster>"
    echo
    echo " add the physical location to the KCP logical cluster"
    exit 1
fi
LOCATION_NAME=$1
CONTEXT=$2

KCP_ROOT="$(dirname "${BASH_SOURCE}")/../.."
DWO_DEMO_ROOT="$(dirname "${BASH_SOURCE}")"
export KCP_DATA_ROOT=${KCP_DATA_ROOT:-$(pwd)}
export KUBECONFIG=${KCP_DATA_ROOT}/.kcp/data/admin.kubeconfig

kubectl --context=${CONTEXT} apply -f ${DWO_DEMO_ROOT}/locations/${LOCATION_NAME}.yaml
