#!/bin/bash

if [ $# -ne 1 ]; then
    echo "add_location.sh <location name> [<logical cluster=admin>]"
    echo
    echo " add the physical location to the KCP logical cluster"
fi
LOCATION_NAME=$1
CONTEXT=${2:-admin}

KCP_ROOT="$(dirname "${BASH_SOURCE}")/../.."
DWO_DEMO_ROOT="$(dirname "${BASH_SOURCE}")"
export KCP_DATA_ROOT=${KCP_DATA_ROOT:-$(pwd)}
export KUBECONFIG=${KCP_DATA_ROOT}/.kcp/data/admin.kubeconfig

kubectl --context=${CONTEXT} apply -f ${DWO_DEMO_ROOT}/${LOCATION_NAME}.yaml
