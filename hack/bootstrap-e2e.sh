#!/usr/bin/env bash

kubectl create namespace garage
kubectl apply -n garage -f garage.yaml

kubectl -n garage wait --for=condition=Ready pod/e2e-garage-0
sleep 5
NODE=$(kubectl -n garage get garagenodes -ojson | jq -r '.items[0].metadata.name')
kubectl -n garage exec -ti e2e-garage-0 -- ./garage layout assign -z e2e -c 50Mi "$NODE"
kubectl -n garage exec -ti e2e-garage-0 -- ./garage layout apply --version 1

pushd config/crd/ || exit
kustomize build | kubectl apply -f -
popd || exit
