#!/bin/sh

namespace=default

function echo_panic(){
    echo -e "\033[45;37m [ $1 ]  \033[0m"
}

function echo_info(){
    echo -e "\033[46;37m [$1] \033[0m"
}

function disable() {
    kubectl label namespace $namespace istio-injection=disabled --overwrite
    status
}

function enable() {
    kubectl label namespace $namespace istio-injection=enabled --overwrite
    status
}

function status() {
    kubectl get namespace -L istio-injection
}

function usage(){
 echo "Usage: $0 {enable|disable|status} "
 echo ""
 echo "Args:  enable  = enable istio inject"
 echo "       disable = enable istio inject"
 echo "       status  = show istio inject for all namespace"
 echo ""
 echo_panic "namespace: $namespace "
 exit 99
}

case "$1" in
  enable|on)
    enable
    ;;
  disable|off)
    disable
    ;;
  status)
    status
    ;;
  help)
    usage
    ;;
  -h)
    usage
    ;;
  *)
  usage
esac