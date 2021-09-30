if [ $# -ne 3 ]; then
    echo "assign_to_location.sh <namespace> <location name> <logical cluster>"
    echo
    echo "Assigns the resource of a KCP namespace to a given location"
    exit 1
fi

NAMESPACE=$1
CLUSTER_NAME=$2
CONTEXT=$3

PATCH='{"metadata":{"labels":{"kcp.dev/cluster":"'${CLUSTER_NAME}'"}}}'

for name in $(kubectl get ingresses -o name -n ${NAMESPACE}); do
kubectl --context=${CONTEXT} patch "$name" -n ${NAMESPACE} --patch=$PATCH
done
for name in $(kubectl get services -o name -n ${NAMESPACE}); do
kubectl --context=${CONTEXT} patch "$name" -n ${NAMESPACE} --patch=$PATCH
done
for name in $(kubectl get configmaps -o name -n ${NAMESPACE}); do
kubectl --context=${CONTEXT} patch "$name" -n ${NAMESPACE} --patch=$PATCH
done
for name in $(kubectl get serviceaccounts -o name  -n ${NAMESPACE}); do
kubectl --context=${CONTEXT} patch "$name" -n ${NAMESPACE} --patch=$PATCH
done
for name in $(kubectl get deployments -o name  -n ${NAMESPACE}); do
kubectl --context=${CONTEXT} patch "$name" -n ${NAMESPACE} --patch=$PATCH
done
for name in $(kubectl get roles -o name  -n ${NAMESPACE}); do
kubectl --context=${CONTEXT} patch "$name" -n ${NAMESPACE} --patch=$PATCH
done
for name in $(kubectl get rolebindings -o name  -n ${NAMESPACE}); do
kubectl --context=${CONTEXT} patch "$name" -n ${NAMESPACE} --patch=$PATCH
done
