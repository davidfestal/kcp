#!/bin/bash

KCP_ROOT="$(dirname "${BASH_SOURCE}")/../.."
DWO_DEMO_ROOT="$(dirname "${BASH_SOURCE}")"
export KCP_DATA_ROOT=${KCP_DATA_ROOT:-$(pwd)}
${KCP_ROOT}/contrib/demo/startKcpAndClusterController.sh -auto_publish_apis=true roles rolebindings persistentvolumeclaims pods ingresses.networking.k8s.io deployments.apps configmaps secrets jobs services serviceaccounts