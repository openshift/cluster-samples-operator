#!/bin/bash

# utility script to store template/imagesteram content from https://github.com/openshift/library
# and store in this repo (cannot access other repos with dist git, and advised against git submodules

wget https://github.com/openshift/library/archive/master.zip -O library.zip
unzip library.zip
find . -name README.md -exec rm {} \;
find . -name index.json -exec rm -f {} \;
pushd library-master
rm -rf .gitignore .travis.yml LICENSE Makefile community* requirements.txt hack *.py *.yaml
popd
rm library.zip
