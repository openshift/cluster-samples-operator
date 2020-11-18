# Overview

The samples operator manages the sample imagestreams and templates stored in the openshift namespace, and any docker credentials, stored as a secret, needed for the imagestreams to import the images they reference.

On initial startup, the operator will create the default samples resource to initiate the creation of the imagestreams and templates.  The imagestreams are the RHEL based OCP imagestreams pointing to images on registry.redhat.access.com.  Similarly the templates are those categorized as OCP templates.  When details for obtaining the credentials for registry.redhat.io are finalized, we will begin defaulting to that registry for the imagestreams.

The samples operator, along with it's configuration resources, are contained within the openshift-cluster-samples-operator namespace. On startup it will copy the pull secret captured by the install into the "openshift" namespace with the name "samples-registry-credentials" to facilitate imagestream imports.  An admin can create any additional secret(s) in the openshift namespace as needed (where those secrets contain the content of a docker config.json) needed to facilitate image import.

The image for the samples operator contains imagestream and template definitions for the associated OpenShift release. Each sample includes an annotation that denotes the OpenShift version that it is compatible with. The operator uses this annotation to ensure that each sample matches it's release version. Samples outside of its inventory are ignored, as are skipped samples (see below). Modifications to any samples that are managed by the operator will be reverted automatically.  The jenkins images are actually part of the image payload from the install and are tagged into the image streams in question directly.



The samples resource includes a finalizer which will clean up the following upon its deletion:

- Operator managed imagestreams
- Operator managed templates
- Operator generated configuration resources
- Cluster status resources
- The samples-registry-credentials secret



Upon deletion of the samples resource, the samples operator will recreate the resource using the default configuration.

# Configuration

The samples resource offers the following configuration fields:

- ManagementState
-- Managed: the operator will update the samples as the configuration dictates
-- Unmanaged: the operator will ignore updates to the samples resource object and any imagestreams or templates in the openshift namespace
-- Removed: the operator will remove the set of managed imagestreams and templates in the openshift namespace. It will ignore new samples created by the cluster admin or any samples in the skipped lists.  After the removals are complete, the operator will work like it is in the 'Unmanaged' state and ignore any watch events on the sample resources, imagestreams, or templates.  There are some caveats around concurrent creates and removal (see Config behaviors section).
- Samples Registry
-- Override the registry that images are imported from
- Architecture
-- Place holder to choose an architecture type.  Currently only x86 is supported.
- Skipped Imagestreams
-- Imagestreams that are in the operator’s inventory, but that the cluster admin wants the operator to ignore / not manage
- Skipped Templates 
-- Templates that are in the operator’s inventory, but that the cluster admin wants the operator to ignore / not manage

## Config restrictions

When we start supporting multiple architectures, the architecture list is not allowed to be changed while in the 'Managed' state.

In order to change the architectures values, an administrator must:
- Mark the ManagementState as 'Removed', saving the change.
- In a subsequent change, change the architecture and change the ManagementState back to Managed.

The operator will still process Secrets while in Removed state.  You can create the secret either before switching to Removed, while in Removed state before switching to Managed state, or after switching to Managed state (though you'll see delays creating the samples until the secret event is processed if you create the secret after switching to Managed). This is done to help facilitate the changing of the registry, where the user chooses to remove all the samples before switching to insure a clean slate (removing before switching is not required).

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
-- This condition is deprecated as of the 4.7 branch of this repository.  `ImageStream` image imports are no longer tracked in real time via conditions on the samples config resource, nor do in progress `ImageStreams` directly affect updates to the `ClusterOperator` instance `openshift-samples`.  Prolonged errors with `ImageStreams` are reported now by Prometheus alerts. 
-- The list of pending imagestreams will be represented by a configmap for each imagestream in the samples operator's namespace (where the imagestream name is the configmap name).  When the imagestream has completed imports, the respective configmap for the imagestream is deleted.
- ImportCredentialsExist
-- Credentials for pulling from `registry.redhat.io` exist in the `pull-secret` Secret in the `openshift-config` namespace
- ConfigurationValid
-- True or false based on whether any of the restricted changes noted above have been submitted
- RemovePending
-- Indicator that we have a management state removed setting pending, resulting in deletion of all the resources the operator manages.  Note that deletions do not start while are waiting for any in progress imagestreams to complete.  This is set to true just prior to executing the deletions, and set to false when the deletions are completed.  The associated ClusterOperator object for the Samples Operator will have its Progressing condition set to `true` when this Condition is `true` (though typically the deletions occur fairly quickly).
- ImportImageErrorsExist
-- Indicator of which imagestreams had errors during the image import phase for one of their tags
-- True when an error has occurred
-- The configmap associated with the imagestream will remain in the samples operator's namespace.  There will be a key in the configmap's data map for each imagestreamtag, where the value of the entry will be the error message reported in the imagestream status.
- MigrationInProgress
-- This condition is deprecated as of the 4.7 branch of this repository.  Upgrade tracking is now achieved via the other conditions and both the per imagestream configmaps and the imagestream-to-image configmap. 

# CVO Cluster Operator Status

- Available
- Failing
- Progressing

See https://github.com/openshift/cluster-version-operator/blob/master/docs/dev/clusteroperator.md#conditions for how these
Cluster Operator status conditions are managed.

# Disconnected mirroring assistance

A `ConfigMap` named `imagestreamtag-to-image` is created in the Samples Operator's namespace that contains an
entry for each `ImageStreamTag`, where the value of the entry is the image used to populate that `ImageStreamTag`.

The format of the key for each entry in the `data` field in the `ConfigMap` is `<imagestreamname>_<imagestreamtagname>`.

Samples Operator will come up as `Removed` for a disconnected OCP install.  If you choose to change it to `Managed`,
override the registry to a mirror registry available in your disconnected environment, you can use this `ConfigMap`
as a reference for which images need to be mirrored for your `ImageStreams` of interest to properly import.

# Troubleshooting

CRD instance for the samples operator config:  `oc get configs.samples.operator.openshift.io cluster -o yaml`

Check the status of the conditions. (See above for details on those conditions)

In the case that a failure occurred during an image import, the Available cluster operator status will be *FALSE*.  A
cluster admin can attempt to rectify the situation by

- Retrying the import via `oc import-image <imagestream name> -n openshift --all`; if successful, the operator will detect the success and clear that imagestream from the failure list
- Add the failing imagestream(s) to the `skippedImagestreams` list; the operator will also clear the imagestream(s) specified from the failure list

Deployment, Events in operator’s namespace (openshift-cluster-samples-operator):  basic `oc get pods`, `oc get events`, `oc logs` of the operator pod 

If any configmaps persist in the samples operator's namespace, the data maps for those configmaps will have information on which 
import failed:  `oc get configmaps -n openshift-cluster-samples-operator -o yaml`

Samples: `oc get is -n openshift`, `oc get templates -n openshift`  … use of -o yaml on any given imagestream should show the status, and import errors will be noted there as well

Deletion of the CRD instance will reset the samples operator to the default configuration, but leave the current revision of the samples operator pod running.

If there is a bug in the samples operator deletion logic, to reset the samples operator configuration by stopping the current pod and starting a new one:
- Run `oc edit clusterversion version` and add an entry for the Deployment of the samples operator so it is unmanaged
- Scale down the deployment via `oc scale deploy cluster-samples-operator --replicas=0`
- Edit the samples resource, remove the finalizer
- Delete the samples resource, config map/secrets in operator namespace, samples in openshift namespace
- Scale up the deployment via `oc scale deploy cluster-samples-operator --replicas=1`


# Update the content in this repository from https://github.com/OpenShift/library
- Create a git branch in your local copy of this repository
- ./library-sync.sh
- Commit the changes and create a PR
