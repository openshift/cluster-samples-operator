#!/bin/bash

# utility script to store template/imagesteram content from https://github.com/openshift/library
# and store in this repo (cannot access other repos with dist git, and advised against git submodules

pushd assets
wget https://github.com/openshift/library/archive/master.zip -O library.zip
unzip library.zip
rm library.zip
find . -name README.md -exec rm {} \;
find . -name index.json -exec rm -f {} \;
pushd library-master
rm -rf arch community* .git* hack import_content.py LICENSE Makefile official* OWNERS requirements.txt .travis.yml
pushd operator
pushd ocp-x86_64
pushd official
mv * ..
popd # official
rmdir official
popd # ocp-x86_64
pushd okd-x86_64
pushd community
mv * ..
popd # community
rmdir community
pushd official
tar cvf ../t.tar *
popd # official
tar xvf t.tar
rm t.tar
rm -rf official
popd # okd-x86_64
pushd ocp-ppc64le
pushd official
mv * ..
popd # official
rmdir official
popd # ocp-ppc64le
popd # operator
tar cvf ../t.tar operator
popd # library-master
git rm -r operator
tar xvf t.tar
git add operator
rm t.tar
rm -rf library-master
