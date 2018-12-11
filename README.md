# Overview

The samples operator manages the sample imagestreams and templates stored in the openshift namespace, and any docker credentials, stored as a secret, needed for the imagestreams to import the images they reference.

On initial startup, the operator will create the default samples resource to initiate the creation of the imagestreams and templates.  Currently, the imagestreams are the CentOS based OKD imagestreams pointing to images on docker.io.  Similarly the templates are those categorized as OKD templates.  When details for obtaining the credentials for registry.redhat.io are finalized, we will begin defaulting to the RHEL and OCP imagestreams and templates.

The samples operator, along with it's configuration resources, are contained within the openshift-cluster-samples-operator namespace. If an admin creates a secret with the name "samples-registry-credentials" which contains the content of a docker config.json file in the operators namespace, it will be copied into the openshift namespace for use during imagestream import.

The image for the samples operator contains imagestream and template definitions for the associated OpenShift release. Each sample includes an annotation that denotes the OpenShift version that it is compatible with. The operator uses this annotation to ensure that each sample matches it's release version. Samples outside of its inventory are ignored, as are skipped samples (see below). Modifications to any samples that are managed by the operator will be reverted automatically.



The samples resource includes a finalizer which will clean up the following upon its deletion:

- Operator managed imagestreams
- Operator managed templates
- Operator generated configuration resources
- Cluster status resources
- A samples-registry-credentials secret (if supplied)



Upon deletion of the samples resource, the samples operator will recreate the resource using the default configuration.

# Configuration

The samples resource offers the following configuration fields:

- ManagementState
-- Managed: the operator will update the samples as the configuration dictates
-- Unmanaged: the operator will ignore updates to the samples resource object and any imagestreams or templates in the openshift namespace
-- Removed: the operator will remove the set of managed imagestreams and templates in the openshift namespace. It will ignore new samples created by the cluster admin or any samples in the skipped lists.  After the removals are complete, the operator will work like it is in the 'Unmanaged' state and ignore any watch events on the samples resources, secrets, imagestreams, or templates.  There are some caveats around concurrent creates and removal (see Change behaviors section).
- Samples Registry
-- Override the registry that images are imported from
- Install Type
-- Use 'centos' for CentOS imagestreams and 'rhel' for RHEL imagestreams (and associatively between OKD and OCP content for the templates)
- Architecture
-- Place holder to choose between x86 and ppc content
- Skipped Imagestreams
-- Imagestreams that are in the operator’s inventory, but that the cluster admin wants the operator to ignore / not manage
- Skipped Templates 
-- Templates that are in the operator’s inventory, but that the cluster admin wants the operator to ignore / not manage

## Config restrictions

The install type and architectures are not allowed to be changed while in the 'Managed' state.

In order to change the install type and architectures values, an administrator must:
- Mark the ManagementState as 'Removed', saving the change
- In a subsequent change, switch the Install Type or Architecture and change the ManagementState back to Managed

## Config behaviors

- Neither deletion nor setting the ManagementState to Removed will be complete while imagestream imports are still in progress.  Once progress has completed (either in success or in error), the delete/removed will commence.  Details on progress tracking can be found in the Conditions section.
- Secret/imagestream/watches events are ignored once deletion or removal has started
- Creation / update of RHEL content will not commence if the secret for pull access is not in place if using the default registry.redhat.io (ie SamplesRegistry is not explicitly set)
- Creation / update of RHEL content will not be gated if the SamplesRegistry has been overridden.
- Secret, imagestream and templates watch events can come in before the initial samples resource object is created … the operator will detect and requeue the event

# Conditions

The samples resource maintains the following conditions in its status:

- SamplesExists
-- Indicates the samples have been created in the openshift namespace
- ImageChangesInProgress
-- True when imagestreams have been created/updated, but not all of the tag spec generations and tag status generations match
-- False when all of the generations match, or unrecoverable errors occurred during import … the last seen error will be in the message field
-- The list of pending imagestreams will be in the reason field
- ImportCredentialsExist
-- A 'samples-registry-credentials' secret has been placed in the operator’s namespace and that secret has been copied into the openshift namespace
- ConfigurationValid
-- True or false based on whether any of the restricted changes noted above have been submitted
- RemovedManagementStateOnHold
-- Indicator that we have a management state removed setting pending, but are waiting for in progress imagestreams to complete

# CVO Cluster Operator Status

- Available - maps to samples exists condition
- Failing - maps to config valid condition
- Progressing - maps to image in progress condition currently but will be updated in the future to account for import failures


# Troubleshooting

Samples Resource:  `oc get samplesresource -o yaml -n openshift-cluster-samples-operator`

Check the status of the conditions. (See above for details on those conditions)

Deployment, Events in operator’s namespace (openshift-cluster-samples-operator):  basic `oc get pods`, `oc get events`, `oc logs` of the operator pod 

Secrets: `oc get secrets` in both openshift and operator’s namespace

Samples: `oc get is -n openshift`, `oc get templates -n openshift`  … use of -o yaml on any given imagestream should show the status, and import errors will be noted there as well

Deletion of the samples resource will reset the samples operator to the default configuration, but leave the current revision of the samples operator pod running.

If there is a bug in the samples operator deletion logic, to reset the samples operator configuration by stopping the current pod and starting a new one:
- Run `oc edit clusterversion version` and add an entry for the Deployment of the samples operator so it is unmanaged
- Scale down the deployment via `oc scale deploy cluster-samples-operator --replicas=0`
- Edit the samples resource, remove the finalizer
- Delete the samples resource, config map/secrets in operator namespace, samples in openshift namespace
- Scale up the deployment via `oc scale deploy cluster-samples-operator --replicas=1`


# Update the content in this repository from https://github.com/OpenShift/library
- Create a git branch in your local copy of this repository
- cd tmp/build
- ./library-sync.sh
- Commit the changes and create a PR
