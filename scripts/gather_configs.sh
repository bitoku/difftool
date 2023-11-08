#!/usr/bin/env bash
set -e

if [ -z "$1" ]; then
  echo "version must be set." 1>&2
  exit 1
fi

LOCATION=eastus
VERSION="$1"
RESOURCE_GROUP=diff-rg-$VERSION
CLUSTER=diff-$VERSION
OUTPUT=default
TIMEOUT=4800
BACKOFF=180

function create_vnet () {
  az group create \
    --name "$RESOURCE_GROUP" \
    --location $LOCATION > /dev/null
  az network vnet create \
    --resource-group "$RESOURCE_GROUP" \
    --name aro-vnet \
    --address-prefixes 10.0.0.0/22 > /dev/null
  az network vnet subnet create \
    --resource-group "$RESOURCE_GROUP" \
    --vnet-name aro-vnet \
    --name worker-subnet \
    --address-prefixes 10.0.0.0/23 > /dev/null
  az network vnet subnet create \
    --resource-group "$RESOURCE_GROUP" \
    --vnet-name aro-vnet \
    --name master-subnet \
    --address-prefixes 10.0.2.0/23 > /dev/null
}

function create_cluster () {
  az aro create \
    --resource-group "$RESOURCE_GROUP" \
    --name "$CLUSTER" \
    --vnet-resource-group "$RESOURCE_GROUP" \
    --vnet aro-vnet \
    --master-subnet master-subnet \
    --worker-subnet worker-subnet \
    --version "$(az aro get-versions --location eastus | jq -r ".[]" | grep "$VERSION" | sort | head -n1)" > /dev/null
}

function fetch_configs () {
  out="$1"
  mkdir -p "$out"
  echo "Created $out"
  oc get dns.operator.openshift.io -o yaml > "$out/dns.yaml"
  oc get proxy.config.openshift.io -o yaml > "$out/proxy.yaml"
  oc get imagecontentsourcepolicy.operator.openshift.io -o yaml > "$out/imagecontentsourcepolicy.yaml"
  oc get kubeletconfig -o yaml > "$out/kubeletconfig.yaml"
  oc get containerruntimeconfig -o yaml > "$out/containerruntimeconfig.yaml"
  oc get mcp -o yaml > "$out/machineconfigpool.yaml"
  oc get configmap -n openshift-monitoring cluster-monitoring-config -o yaml > "$out/cluster-monitoring-config.yaml"
  oc get machine -n openshift-machine-api -l machine.openshift.io/cluster-api-machine-role=master -o yaml > "$out/master-machines.yaml"
  echo "Fetch done"
}

function wait_and_fetch () {
  current_version=$(oc get clusterversion -ojson | jq -r ".items[0].status.desired.version")
  elapsed=0
  while [ "$(oc get clusterversion -ojson | jq -r ".items[0].status.history[0].state == \"Completed\"")" != "true" ]; do
    if [ $elapsed -gt $TIMEOUT ]; then
      echo "ERROR: timeout" 1>&2
      exit 1
    fi
    sleep $BACKOFF
    elapsed=$((elapsed + BACKOFF))
    oc get clusterversion -ojson | jq -r ".items[0].status.conditions[] | select(.type == \"Progressing\").message"
  done
  echo "update done"
  fetch_configs $OUTPUT/"$current_version"
}

if ! az aro show -g "$RESOURCE_GROUP" -n "$CLUSTER" &> /dev/null; then
  echo "cluster not found. Creating"
  create_vnet
  create_cluster
fi

api_server=$(az aro show -g "$RESOURCE_GROUP" -n "$CLUSTER" | jq -r '.apiserverProfile.url')
password=$(az aro list-credentials -g "$RESOURCE_GROUP" -n "$CLUSTER" | jq -r '.kubeadminPassword')

oc login \
  --server "$api_server" \
  --username kubeadmin \
  --password "$password"

if [ "$(oc get clusterversion -ojson | jq -r ".items[0].spec.channel")" == "null" ]; then
  echo "set channel"
  oc patch clusterversion version --type="merge" -p "{\"spec\": {\"channel\": \"stable-$VERSION\"}}"
fi

wait_and_fetch

for v in $(oc get clusterversion -ojson | jq -r ".items[0].status.availableUpdates[].version" | sort); do
  oc adm upgrade --to="$v"
  while [ "$(oc get clusterversion -ojson | jq -r ".items[0].status.history[0].state == \"Completed\"")" == "true" ]; do : ;done
  echo "update cluster to $v"
  wait_and_fetch
done
