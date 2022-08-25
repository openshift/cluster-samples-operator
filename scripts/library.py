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
parser.add_argument("--issueTitle", help="Store the Repo Type")
parser.add_argument("--issueDescription", help="Store the Repo Type")
args = parser.parse_args()

user_input = args.repoType
issueTitle = args.issueTitle
issueDescription = args.issueDescription
print("Selected Repo Type: ",user_input)

try:
    from yaml import CLoader as Loader, CDumper as Dumper
except ImportError:
    from yaml import Loader, Dumper


imageStreamDict = {}
templateDict = {}
combinedDict = {}
testimagestreamsDict = {"testimage":["fbm3307/test-learn"], "testimagest":["fbm3307/testimagestreams1"]}
testtemplatesDict = {"testtemplate":["fbm3307/testtemplates"]}
testallDict = {"testimage":["fbm3307/test-learn"], "testimagest":["fbm3307/testimagestreams1"], "testtemplate":["fbm3307/testtemplates"]}

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
                finalUrl = f"{str(repo1)}/{str(repo2)}"
                templateLocationSet.add(finalUrl)
            templateDict[reponame] = list(templateLocationSet)
        imagestreamLocationSet.update(templateLocationSet)
        combinedDict[reponame] = list(imagestreamLocationSet)
print("completed the division of the  repos into imagestreams and templates")

if(user_input == "all" or user_input == "templates" or user_input == "imagestreams"):
    load_yaml()

targetDict = {}
if(user_input == "all"):
    targetDict = combinedDict
    print("Going to create the issue in ALL combined target repos")
elif(user_input == "templates"):
    targetDict = templateDict
    print("Going to create the issue in Template target repos")
elif(user_input == "imagestreams"):
    targetDict = imageStreamDict
    print("Going to create the issue in Imagestreams target repos")
elif(user_input == "testimagestreams"):
    targetDict = testimagestreamsDict
    print("Going to create the issue in Testimagestreams target repos")
elif(user_input == "testtemplates"):
    targetDict = testtemplatesDict
    print("Going to create the issue in TestTemplate target repos")
elif(user_input == "testall"):
    targetDict = testallDict
    print("Going to create the issue in TestAll target repos")
else:
    print("Invalid input")
    exit()

for repoName in targetDict.keys():
    repoList = targetDict[repoName]
    print("Initiating creation of issues in Repos : ", repoList)
    for repo in repoList:
        isSuccess = create_an_issue(title=issueTitle,description=issueDescription, repo=str(repo))
        if(isSuccess):
            print("Created the issues in: ", repo)
        else:
            print("Error while creating issue in ", repo)