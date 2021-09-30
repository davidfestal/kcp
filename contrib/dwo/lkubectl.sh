#!/bin/bash

if [ $# -eq 0 ]; then
    echo "lkubectl.sh <location name> ..."
    echo
    echo " runs a kubectl command against the given physical cluster of the given location"
    exit 1
fi

KCP_ROOT="$(dirname "${BASH_SOURCE}")/../.."
DWO_DEMO_ROOT="$(dirname "${BASH_SOURCE}")"
LOCATION_NAME=$1
shift

kubectl --kubeconfig=${DWO_DEMO_ROOT}/locations/${LOCATION_NAME}.kubeconfig $*
