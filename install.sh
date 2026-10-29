#!/bin/sh

readonly yaml=charts/spegel.yaml

helm -n kube-system template spegel charts/spegel >"$yaml"
if [ $# -ne 0 ]; then
  sed 's~/run/containerd/~/run/k3s/containerd/~g' "$yaml" |
    sed 's~/etc/containerd/~/var/lib/rancher/k3s/agent/etc/containerd/~g' |
    sed 's~/var/lib/containerd/~/var/lib/rancher/k3s/agent/containerd/~g' >sed.t
  mv sed.t "$yaml"
fi
