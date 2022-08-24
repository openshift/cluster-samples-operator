from os import access
import requests
import json
import os
endpoint_data = {}


def _set_endpoint_key(key, env_var):
    if key not in endpoint_data:
        if env_var in os.environ:
            endpoint_data[key] = os.environ[env_var]
        else:
            raise Exception(
                f"Environment variables {env_var} is required to connect to github")


def _make_gihub_request(method="post", uri="issues", body=None, params={}, headers={}, verbose=False, repo=""):
    GITHUB_BASE_URL = "https://api.github.com"
    headers.update({"Authorization": f'Bearer {os.environ["GITHUB_TOKEN"]}',
                    "Accept": "application/vnd.github.v3+json"})    
    print(headers)
    url = f'{GITHUB_BASE_URL}/repos/{repo}/{uri}'
    print(f"API url: {url}")
    request_method = requests.post
    response = request_method(url, params=params, headers=headers, json=body)
    try:
        response.raise_for_status()
    except Exception as e:
        print("Exeption : ", e)
    try:
        resp_json = response.json()
    except Exception:
        resp_json = None
    if resp_json and verbose:
        print(json.dumps(resp_json, indent=4, sort_keys=True))
    return resp_json

def create_an_issue(title, description="description", repo=""):
    uri = "issues"
    method = "post"
    body = {"title": title,
            "body": description
            }
    _make_gihub_request(method, uri, body=body, verbose=False, repo=repo)

