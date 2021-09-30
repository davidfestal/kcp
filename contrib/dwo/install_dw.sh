#!/bin/bash

if [ $# -ne 2 ]; then
    echo "install_dw.sh <routing suffix> <logical cluster>"
    echo
    echo " installs the DevWorkspace controller related resources (CRDs, etc ...) to the given KCP logical cluster"
    exit 1
fi
ROUTING_SUFFIX=$1
CONTEXT=$2

KCP_ROOT="$(dirname "${BASH_SOURCE}")/../.."
DWO_DEMO_ROOT="$(dirname "${BASH_SOURCE}")"
if [[ ${DWO_ROOT} == "" ]]; then
  echo "Set the DWO root"
  exit 1
fi

export KCP_DATA_ROOT=${KCP_DATA_ROOT:-$(pwd)}
export KUBECONFIG=${KCP_DATA_ROOT}/.kcp/data/admin.kubeconfig

if [[ "$(kubectl api-resources --api-group='route.openshift.io'  2>&1 | grep -o routes)" != "" ]]; then
  PLATFORM=openshift
else
  PLATFORM=kubernetes
fi

export DEVWORKSPACE_CONTROLLER_NAMESPACE=devworkspace-controller
kubectl --context=${CONTEXT} create namespace ${DEVWORKSPACE_CONTROLLER_NAMESPACE}

for file in $(ls ${DWO_ROOT}/deploy/deployment/${PLATFORM}/objects/*.yaml | grep -v -E 'Deployment|Certificate|Issuer|devworkspace-controller-metrics'); do
  kubectl --context=${CONTEXT} apply -f $file
done
kubectl --context=${CONTEXT} patch configmap devworkspace-controller-configmap -n ${DEVWORKSPACE_CONTROLLER_NAMESPACE} --patch='{"data":{"devworkspace.routing.cluster_host_suffix":"'${ROUTING_SUFFIX}'"}}'
kubectl --context=${CONTEXT} patch crds devworkspaces.workspace.devfile.io --patch='{"spec":{"conversion":{"strategy":"None","webhook":null}}}'
kubectl --context=${CONTEXT} patch crds devworkspacetemplates.workspace.devfile.io --patch='{"spec":{"conversion":{"strategy":"None","webhook":null}}}'
