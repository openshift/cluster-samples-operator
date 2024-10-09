#!/bin/bash

# utility script to gather template/imagestream content from https://github.com/openshift/library
# and store it in this repo (cannot access other repos with dist git, and advised against git submodules

# Without any commandline arguments, this script only updates the OCP supported samples. This behavior
# can be modified using these commandline arguments:
# * --okd - the OKD samples are updated
# * --ocp - the OCP supported samples are updated
# * --ocp-all - all OCP samples are updated including the unsupported ones

###########################################

# process the commandline args
PROCESS_OKD="false"
PROCESS_ALL_OCP_SAMPLES="false"
if [[ $# -eq 0 ]]; then
  PROCESS_OCP="true"
else
  PROCESS_OCP="false"
fi

while [[ $# -gt 0 ]]; do
  case $1 in
  --okd)
    PROCESS_OKD="true"
    ;;
  --ocp)
    PROCESS_OCP="true"
    ;;
  --ocp-all)
    PROCESS_OCP="true"
    PROCESS_ALL_OCP_SAMPLES="true"
    ;;
  esac
  shift
done

# set up variables
OCP_SUPPORTED_SAMPLES="ruby python nodejs perl php httpd nginx eap java webserver dotnet golang rails"
OCP_ARCHS="ocp-x86_64 ocp-aarch64 ocp-ppc64le ocp-s390x"
OKD_ARCHS="okd-x86_64"
ALL_ARCHS="$OCP_ARCHS $OKD_ARCHS"
if $PROCESS_OKD; then
  if $PROCESS_OCP; then
    PROCESSED_ARCHS=$ALL_ARCHS
  else
    PROCESSED_ARCHS=$OKD_ARCHS
  fi
else
  PROCESSED_ARCHS=$OCP_ARCHS
fi

# helper functions
function reset_directory() {
  git checkout HEAD -- "$1"
  # remove any changes from the working tree in this directory that checkout left behind
  git stash -a -- "$1"
  git stash drop
}

function reset_ocp_unsupported() {
  for d in $(ls); do
    if [[ "${OCP_SUPPORTED_SAMPLES}" != *"${d}"* ]]; then
      reset_directory "${d}"
    fi
  done
}

# process the openshift library
pushd assets
wget https://github.com/openshift/library/archive/master.zip -O library.zip
unzip library.zip
rm library.zip
pushd library-master
rm -rf api arch cmd community* .git* hack official* vendor Dockerfile LICENSE Makefile OWNERS README.md go.* main.go
pushd operator
pushd ocp-x86_64
pushd official
mv * ..
popd # official
rmdir official
popd # ocp-x86_64
pushd ocp-aarch64
pushd official
mv * ..
popd # official
rmdir official
popd #ocp-aarch64
pushd ocp-ppc64le
pushd official
mv * ..
popd # official
rmdir official
popd #ocp-ppc64le
pushd ocp-s390x
pushd official
mv * ..
popd # official
rmdir official
popd #ocp-s390x
pushd okd-x86_64
pushd community
mv * ..
popd # community
rmdir community
pushd official
cp -r * ..
popd # official
rm -rf official
popd #okd-x86_64
popd # operator
tar cvf ../t.tar operator
popd # library-master
git rm -r operator
tar xvf t.tar
git add operator
rm t.tar
rm -rf library-master

pushd operator
for arch in $ALL_ARCHS; do
  if [[ "${PROCESSED_ARCHS}" == *"${arch}"* ]]; then
    # There are no unsupported samples in OKD, but we need
    # to reset the unsupported samples in OCP.
    if ! ${PROCESS_ALL_OCP_SAMPLES}; then
      if [[ "${OCP_ARCHS}" == *"${arch}"* ]]; then
        pushd "${arch}"
        reset_ocp_unsupported
        popd # $arch
      fi
    fi
  else
    # we're not supposed to update this arch.
    reset_directory "${arch}"
  fi
done

popd # operator
