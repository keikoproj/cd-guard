# cd-guard
[![codecov](https://codecov.io/gh/keikoproj/cd-guard/branch/main/graph/badge.svg)](https://codecov.io/gh/keikoproj/cd-guard)

Continuous Delivery Guard is to protected the deployment, make sure the deployment are going well. 
It includes bunch of validations which OPA can not cover, for example, HPA, Ingress and PDB validations
It needs to valid multiple objects and do small extra modification.

- Case 1: HPA + Deployment or Rollout validation
- Case 2: Ingress + Deployment or Rollout with PodReadinessGate validation for IKS2.0
- Case 3: Check whether the PDB spec in-place for Deployment in production

# The problem it resolves
The original [Kubenertes issue](https://github.com/kubernetes/kubernetes/issues/25238)

# Why OPA or Admission Controller doesn't work for this case
The policies across multiple objects. Also HPA guard needs to make a slight change on liveObject

# HPA validations
1.  Check whether there is any HorizontalPodAutoscaler
    - If YES, check whether the target is a **Deployment**
        - If YES, goto step 2
        - If NO, return
    - If NO,  return
2. Check whether the Deployment has **replicas**
    - If YES, show error, suggest to delete the replicas
    - If NO, goto step 3
3. Check whether the old Deployment has **replicas**
   - If YES, run "kubectl apply set-last-applied -f deployment.yaml -n ${THE_NAMESPACE}", deployment.yaml  is the **OLD** Deployment Spec with no replicas
   - If NO, return

# Ingress Validations
1. Check whether there is any Ingress
   - If YES, go through all of them check whether has annotation "alb.ingress.kubernetes.io/target-type=ip", if there is no target-type ip Ingress, then return
   - If NO, return
2. Go through all "Deployment" or "Rollout" check whether they have "PodReadinessGate",
   Mark the pointed Ingress to Referred.
3. Show error when there is Ingress with target-type=ip and no referral.


# How to use this command line?

1. It should be used after "argocd app creation" and before "argocd sync"
1. The argocd config and namespace should be specified.

** sh "/cd-guard all ${appName}-${envName}"**

In Jenkinsfile
```
        container('cdtools') {
            println("Deploying to ${appName}")
            withCredentials([usernamePassword(credentialsId: 'github-svc-sbseg-ci', passwordVariable: 'GIT_PASSWORD', usernameVariable: 'GIT_USERNAME')]) {
            //.....
            sh("/argocd login ${argocd_server} --name context --insecure  --username admin --password $ARGOCD_PASS")
            def cluster = clusterMap[envName]
            sh "/argocd app create --name ${appName}-${envName} --repo https://${deploy_repo} --path environments/${envName}  --dest-server ${cluster} --dest-namespace ${appName}-${envName} --upsert"
            sh "/cd-guard all ${appName}-${envName}"
            sh "/argocd app sync ${appName}-${envName}"
            sh "/argocd app wait ${appName}-${envName} --timeout ${app_wait_timeout}"
        }
```

# HPA guard covers following cases

|   | To Replicas  | To HPA, has replicas  | To HPA, no replicas  |
|---|---|---|---|
| Was replicas  | NONE   | Error | Apply change|
| Was HPA   |NONE| Error|Good|
