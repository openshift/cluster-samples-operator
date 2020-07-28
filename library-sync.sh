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
rmdir official community
popd #okd-x86_64
popd # operator
tar cvf ../t.tar operator
popd # library-master
git rm -r operator
tar xvf t.tar
git add operator
rm t.tar
rm -rf library-master

echo "REMEMBER TO PATCH THE DATAGRID TEMPLATES TO apps/v1 FOR THEIR StatefulSets INSTANCES,"
echo " and fix openjdk-8-rhel8:1.1 until jboss fixes,"
echo " and nodejs* templates off of v8"
