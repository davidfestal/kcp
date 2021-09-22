#!/bin/bash

KCP_ROOT="$(dirname "${BASH_SOURCE}")/../.."
DWO_DEMO_ROOT="$(dirname "${BASH_SOURCE}")"
if [[ ${DWO_ROOT} == "" ]]; then
  echo "Set the DWO root"
  exit 1
fi
CONTEXT=${1:-admin}


export KCP_DATA_ROOT=${KCP_DATA_ROOT:-$(pwd)}
export KUBECONFIG=${KCP_DATA_ROOT}/.kcp/data/admin.kubeconfig

if [[ "$(kubectl api-resources --api-group='route.openshift.io'  2>&1 | grep -o routes)" != "" ]]; then
  PLATFORM=openshift
else
  PLATFORM=kubernetes
fi

export DEVWORKSPACE_CONTROLLER_NAMESPACE=devworkspace-controller

export CONTROLLER_CONFIG_MAP_NAMESPACE=${DEVWORKSPACE_CONTROLLER_NAMESPACE}
export DISABLE_WEBHOOKS=true
export MAX_CONCURRENT_RECONCILES=5
export RELATED_IMAGE_project_clone=quay.io/devfile/project-clone:next
export RELATED_IMAGE_pvc_cleanup_job=registry.access.redhat.com/ubi8-micro:8.4-81
export RELATED_IMAGE_async_storage_server=quay.io/eclipse/che-workspace-data-sync-storage:0.0.1
export RELATED_IMAGE_async_storage_sidecar=quay.io/eclipse/che-sidecar-workspace-data-sync:0.0.1
export RELATED_IMAGE_plugin_redhat_developer_web_terminal_4_5_0=quay.io/eclipse/che-machine-exec:nightly
export RELATED_IMAGE_web_terminal_tooling=quay.io/wto/web-terminal-tooling:latest
ramtmp="$(mktemp -p /dev/shm/)"
trap "rm -f '$ramtmp'" exit
sed -e "s/^current-context: .*$/current-context: ${CONTEXT}/" ${KUBECONFIG} > $ramtmp
${DWO_ROOT}/bin/manager --kubeconfig $ramtmp
