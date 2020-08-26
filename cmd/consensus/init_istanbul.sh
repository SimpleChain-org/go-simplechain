#!/bin/bash
set -u
set -e

numNodes=7
dir=pbftdata
ips=()
ports=()
discports=()
validators=()

function usage() {
  echo ""
  echo "Usage:"
  echo "    $0 [--numNodes numberOfNodes --ip ipList --port portList"
  echo ""
  echo "Where:"
  echo "    numberOfNodes is the number of nodes to initialise (default = $numNodes)"
  echo "    ip is the ipList of nodes (default = 127.0.0.1 127.0.0.1 127.0.0.1 ...)"
  echo "    port is the portList of nodes (default = 21001 21002 21003 ...)"
  echo ""
  exit 0
}

function extra() {
  go build
  cmd="./consensus pbft extra"
  for i in $(seq 1 "${numNodes}"); do
    cmd="${cmd} --validator=${validators[i]}"
  done
  $cmd
  exit 0
}

while (("$#")); do
  case "$1" in
  --numNodes)
    re='^[0-9]+$'
    if ! [[ $2 =~ $re ]]; then
      echo "ERROR: numberOfNodes value must be a number"
      usage
    fi
    numNodes=$2
    shift 2
    ;;
  --help)
    shift
    usage
    ;;
  --ip)
    for i in $(seq 1 "${numNodes}"); do
      shift 1
      ips[i]=$1
    done
    shift 1
    ;;
  --port)
    for i in $(seq 1 "${numNodes}"); do
      shift 1
      ports[i]=$1
    done
    shift 1
    ;;
  --discport)
    for i in $(seq 1 "${numNodes}"); do
      shift 1
      discports[i]=$1
    done
    shift 1
    ;;
  --validator)
    for i in $(seq 1 "${numNodes}"); do
      shift 1
      validators[i]=$1
    done
    extra
    ;;
  *)
    echo "Error: Unsupported command line parameter $1"
    usage
    ;;
  esac
done

defaultIp=127.0.0.1
defaultPort=21000

if test ${#ips[*]} -eq 0; then
  for i in $(seq 1 "${numNodes}"); do
    ips[i]=$defaultIp
  done
fi

if test ${#ports[*]} -eq 0; then
  for i in $(seq 1 "${numNodes}"); do
    port=$((defaultPort + i))
    ports[i]=$port
  done
fi

echo "[*] Cleaning up temporary data directories"
rm -rf ${dir}
mkdir -p ${dir}/logs

echo "[*] Configuring for $numNodes node(s)"
echo "$numNodes" >${dir}/numberOfNodes

go build

cmd="./consensus pbft generate --nodedir=${dir}/nodekey --n=${numNodes} --genesis=genesis_istanbul.json"

for i in $(seq 1 "${numNodes}"); do
  cmd="${cmd} --ip=${ips[i]} --port=${ports[i]}"
done

if test ${#discports[*]} -eq "${numNodes}"; then
  for i in $(seq 1 "${numNodes}"); do
    cmd="${cmd} --discport=${discports[i]}"
  done
fi

$cmd

echo "[*] Configuring pbft node(s) successful"

for i in $(seq 1 "${numNodes}"); do
  mkdir -p ${dir}/dd"${i}"/{keystore,sipe}
  cp ${dir}/nodekey/static-nodes.json ${dir}/dd"${i}"/static-nodes.json
  cp ${dir}/nodekey/nodekey"${i}" ${dir}/dd"${i}"/sipe/nodekey
  cp ${dir}/nodekey/keys/key"${i}" ${dir}/dd"${i}"/keystore/key"${i}"
  sipe init ${dir}/nodekey/genesis_pbft.json --datadir=${dir}/dd"${i}" --role=subchain
done
