#!/bin/bash

# utility script to gather template/imagestream content from https://github.com/openshift/library
# and store it in this repo (cannot access other repos with dist git, and advised against git submodules

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

SUPPORTED="ruby python nodejs perl php httpd nginx eap java webserver dotnet golang"
function reset_unsupported() {
  for d in $(ls); do
    if [[ "${SUPPORTED}" != *"${d}"* ]]; then
      git reset HEAD -- "${d}"
      # remove any changes from the working tree in this directory that reset left behind
      git stash -u -- "${d}"
      git stash drop
    fi
  done
}

ARCHS="ocp-x86_64 ocp-aarch64 ocp-ppc64le ocp-s390x okd-x86_64"
pushd operator
for arch in $ARCHS; do
  pushd "${arch}"
  reset_unsupported
  popd # $arch
done
popd # operator
