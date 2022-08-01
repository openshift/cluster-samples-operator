from email.mime import image
import os
from re import template
import sys
from typing import final
import yaml
try:
    from yaml import CLoader as Loader, CDumper as Dumper
except ImportError:
    from yaml import Loader, Dumper

LIBRARY_FILE='/Users/fmehta/Projects/library/official.yaml'


if not os.path.exists(LIBRARY_FILE):
    print(f"[ERROR] {LIBRARY_FILE} file does not exist.")
else:
    loaddata = open(LIBRARY_FILE).read()
    filedata = yaml.safe_load(loaddata)
    githubcontent=filedata["data"]
    imageStreamDict = {}
    templateDict = {}
    combinedDict = {}
    for reponame in githubcontent:
        imagestreamLocationSet = set() #Initialize the locationSet
        if("imagestreams" in githubcontent[reponame]):
            #code for imagestream
            for ele in githubcontent[reponame]["imagestreams"]:
                location = ele["location"]
                temp = (str(location).split("//")[1]).split("/")
                one,two = temp[1], temp[2]
                finalUrl = "https://github.com/" + str(one) +"/" + str(two)
                imagestreamLocationSet.add(finalUrl)
            imageStreamDict[reponame] = list(imagestreamLocationSet)
        templateLocationSet = set() #Re-Initialize the location list
        if("templates" in githubcontent[reponame]):
            #code for templates
            for ele in githubcontent[reponame]["templates"]:
                location = ele["location"]
                temp = (str(location).split("//")[1]).split("/")
                one,two = temp[1], temp[2]
                finalUrl = "https://github.com/" + str(one) +"/" + str(two)
                templateLocationSet.add(finalUrl)
            templateDict[reponame] = list(templateLocationSet)
        imagestreamLocationSet.update(templateLocationSet)
        combinedDict[reponame] = list(imagestreamLocationSet)
        print(reponame)
        if("imagestreams" in githubcontent[reponame]):
            print("imagestream", imageStreamDict[reponame])
        if("templates" in githubcontent[reponame]):
            print("templates", templateDict[reponame])
        print("combined", combinedDict[reponame])
        #input("Press")
    print("Out of loop)")
    input("Press")
    print("Template", templateDict)
    print("imageStreams", imageStreamDict)
    print("combined", combinedDict)