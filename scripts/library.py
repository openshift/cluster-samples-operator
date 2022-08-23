from email.mime import image
import os
from re import template
import sys
from typing import final
import yaml
import requests
import argparse
from create_issue import *

parser = argparse.ArgumentParser()
parser.add_argument("--repoType", help="Store the Repo Type")
args = parser.parse_args()

user_input = args.repoType
print("Selected Repo Type: ",user_input)

try:
    from yaml import CLoader as Loader, CDumper as Dumper
except ImportError:
    from yaml import Loader, Dumper


imageStreamDict = {}
templateDict = {}
combinedDict = {}
testDict = {"test":["fbm3307/test-learn"]}

def load_yaml():
    global imageStreamDict
    global templateDict
    global combinedDict

    print("Loading the repo list from official.yaml")

    LIBRARY_FILE= requests.get("https://github.com/openshift/library/blob/master/official.yaml?raw=true")
    filedata = yaml.safe_load(LIBRARY_FILE.text)
    githubcontent=filedata["data"]
    
    for reponame in githubcontent:
        imagestreamLocationSet = set() #Initialize the locationSet
        if("imagestreams" in githubcontent[reponame]):
            #code for imagestream
            for ele in githubcontent[reponame]["imagestreams"]:
                location = ele["location"]
                temp = (str(location).split("//")[1]).split("/")
                repo1,repo2 = temp[1], temp[2]
                #finalUrl = "https://github.com/" + str(repo1) +"/" + str(repo2)
                finalUrl = f"{str(repo1)}/{str(repo2)}"
                imagestreamLocationSet.add(finalUrl)
            imageStreamDict[reponame] = list(imagestreamLocationSet)
        templateLocationSet = set() #Re-Initialize the location list
        if("templates" in githubcontent[reponame]):
            #code for templates
            for ele in githubcontent[reponame]["templates"]:
                location = ele["location"]
                temp = (str(location).split("//")[1]).split("/")
                repo1,repo2 = temp[1], temp[2]
                #finalUrl = "https://github.com/" + str(repo1) +"/" + str(repo2)
                finalUrl = f"{str(repo1)}/{str(repo2)}"
                templateLocationSet.add(finalUrl)
            templateDict[reponame] = list(templateLocationSet)
        imagestreamLocationSet.update(templateLocationSet)
        combinedDict[reponame] = list(imagestreamLocationSet)
print("completed the division of the  repos into imagestreams and templates")
load_yaml()
targetDict = {}
if(user_input == "all"):
    targetDict = combinedDict
    print("Going to create the issue in target repos")
elif(user_input == "templates"):
    targetDict = templateDict
    print("Going to create the issue in target repos")
elif(user_input == "imagestreams"):
    targetDict = imageStreamDict
    print("Going to create the issue in target repos")
elif(user_input == "test"):
    targetDict = testDict
    print("Going to create the issue in target repos")
else:
    print("Invalid input")
    exit()
for repoName in targetDict.keys():
    repoList = targetDict[repoName]
    for repo in repoList:
        create_an_issue(title="sample issue",description="sample description", repo=str(repo))
        print("created the issues in target repos")