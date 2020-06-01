/*
Copyright 2018 The Kubernetes Authors.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0

Unless required by applicable law or agreed to in writing, software
distributed under the License is distributed on an "AS IS" BASIS,
WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
See the License for the specific language governing permissions and
limitations under the License.
*/

package cmd

import (
	"context"
	"fmt"
	"os"
	"reflect"
	"strconv"
	"strings"

	"github.com/argoproj/argo-cd/util"
	"github.com/spf13/cobra"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/util/json"
	"k8s.io/client-go/tools/clientcmd/api"

	"k8s.io/cli-runtime/pkg/genericclioptions"

	argocdclient "github.com/argoproj/argo-cd/pkg/apiclient"
	"github.com/argoproj/argo-cd/pkg/apiclient/application"
	argoappv1 "github.com/argoproj/argo-cd/pkg/apis/application/v1alpha1"
	log "github.com/sirupsen/logrus"
)

var (
	//The command requires $ARGOCD_SERVER $APP_NAME in environment
	guardExample = `
	# List all support guards
	%[1]s

	# Guard given aspect'
	%[1]s <aspect>

    # Verify given aspect, but won't change anything'
    %[1]s <aspect> --dryRun
    `

	//errNoContext = fmt.Errorf("no context is currently set, use %q to select a new one", "kubectl config use-context <context>")
)

// GuardOptions provides information required to update
// the current context on a user's KUBECONFIG
type GuardOptions struct {
	configFlags *genericclioptions.ConfigFlags

	resultingContext     *api.Context
	resultingContextName string

	userSpecifiedCluster  string
	userSpecifiedContext  string
	userSpecifiedAuthInfo string
	userSpecifiedGuard    string

	rawConfig api.Config
	args      []string

	dryRun bool

	clientOpts *argocdclient.ClientOptions
}

// NewGuardOptions provides an instance of GuardOptions with default values
func NewGuardOptions(clientOpts *argocdclient.ClientOptions) *GuardOptions {
	return &GuardOptions{
		configFlags: genericclioptions.NewConfigFlags(),

		clientOpts: clientOpts,
	}
}

// NewCmdGuard provides a cobra command wrapping GuardOptions
func NewCmdGuard(clientOpts *argocdclient.ClientOptions) *cobra.Command {
	o := NewGuardOptions(clientOpts)

	var cmd = &cobra.Command{
		Use:     "[guard1,guard2] [flags]",
		Short:   "List or execute guards",
		Example: fmt.Sprintf(guardExample, "cd-guard hpa"),
		Run: func(c *cobra.Command, args []string) {
			c.HelpFunc()(c, args)
			os.Exit(0)
		},
	}

	cmd.AddCommand(NewGuardHpaCommand(clientOpts))
	cmd.AddCommand(NewGuardIngressCommand(clientOpts))
	cmd.AddCommand(NewGuardAllCommand(clientOpts))

	cmd.Flags().BoolVar(&o.dryRun, "dryRun", o.dryRun, "if true, guard just verify, won't make any change")

	//Ignore unknown flags
	cmd.Flags().ParseErrorsWhitelist.UnknownFlags = true
	cmd.PersistentFlags().ParseErrorsWhitelist.UnknownFlags = true
	cmd.FParseErrWhitelist.UnknownFlags = true
	cmd.DisableFlagParsing = true

	o.configFlags.AddFlags(cmd.Flags())

	return cmd
}

func verifyHpa(resourceDiffs []*argoappv1.ResourceDiff) ([]*unstructured.Unstructured, []string, map[string]*argoappv1.ResourceDiff, int) {
	hpas, resourceNames, resources := hpaReferencesObjects(resourceDiffs)

	if len(hpas) == 0 {
		log.Infof("No HPA found, good to pass through")
		return nil, nil, nil, 200
	}

	for i := range resourceNames {
		resourceName := resourceNames[i]
		resource := resources[resourceName]
		if resource == nil {
			log.Errorf("The HPA:%s refer to a non-exists resource: %s", hpas[i].GetName(), resourceName)
			os.Exit(301)
			return nil, nil, nil, 301
		}

		resourceTarget, error := resource.TargetObject()
		if error != nil || resourceTarget == nil {
			log.Errorf("The target object %s doesn't exist or has error %v", resourceName, error)
			return nil, nil, nil, 200
		}
		if error == nil {
			specObj := resourceTarget.Object["spec"]
			if specObj != nil && reflect.TypeOf(specObj).String() == "map[string]interface {}" {
				spec := specObj.(map[string]interface{})
				if spec["replicas"] != nil {
					log.Errorf("Please set 'spec.replicas' as null ('replicas: null') in %s:%s for kustomize template or delete 'spec.replicas' if you use ksonnet, since the replicas is managed by HPA:%s", resource.Kind, resourceName, hpas[i].GetName())
					os.Exit(302)
					return nil, nil, nil, 301
				}
			}
		}

		resourceLive, error := resource.LiveObject()
		if error != nil {
			log.Errorf("The live object has error %v", error)
			return nil, nil, nil, 200
		}
		if error == nil {
			if resourceLive == nil { //No object, first time roll out
				delete(resources, resourceName)
				resourceNames[i] = ""
				continue
			}

			var metadataObj = resourceLive.Object["metadata"]
			if metadataObj != nil && reflect.TypeOf(metadataObj).String() == "map[string]interface {}" {
				metadata := metadataObj.(map[string]interface{})
				if metadata["annotations"] != nil && reflect.TypeOf(metadata["annotations"]).String() == "map[string]interface {}" {
					annotations := metadata["annotations"].(map[string]interface{})

					var lastAppliedConfiguration = annotations["kubectl.kubernetes.io/last-applied-configuration"]
					if lastAppliedConfiguration != "" {
						var resourceLastApplied = &unstructured.Unstructured{}
						err := json.Unmarshal([]byte(lastAppliedConfiguration.(string)), resourceLastApplied)
						if err == nil {
							specObj := resourceLastApplied.Object["spec"]
							if specObj != nil && reflect.TypeOf(specObj).String() == "map[string]interface {}" {
								spec := specObj.(map[string]interface{})
								if spec["replicas"] == nil { //The Deployment/Rollout doesn't have 'spec.replicas' it is in good state
									log.Infof("%s:%s doesn't have 'spec.replicas', it is managed by HPA, perfect!", resource.Kind, resourceName)
									delete(resources, resourceName)
									resourceNames[i] = ""
									continue
								}
							}
						}
					}
				}
			}
		}
	}
	return hpas, resourceNames, resources, 0
}

func getRefreshType(refresh bool, hardRefresh bool) *string {
	if hardRefresh {
		refreshType := string(argoappv1.RefreshTypeHard)
		return &refreshType
	}

	if refresh {
		refreshType := string(argoappv1.RefreshTypeNormal)
		return &refreshType
	}

	return nil
}

// NewGuardAllCommand returns a new instance of an `argocd app create` command
func NewGuardAllCommand(clientOpts *argocdclient.ClientOptions) *cobra.Command {
	var dryRun bool
	var timeout uint

	var allCmd = &cobra.Command{
		Use:     "all [flags]",
		Short:   "Execute all guards",
		Example: fmt.Sprintf(guardExample, "guard all"),
		Run: func(c *cobra.Command, args []string) {
			for _, cmd := range c.Parent().Commands() {
				if cmd.Name() != "all" && cmd.Name() != "help" {
					cmd.Run(cmd, args)
				}
			}
		},
	}
	allCmd.Flags().BoolVar(&dryRun, "dryRun", false, "If true, it won't make any changes.")
	allCmd.Flags().UintVar(&timeout, "timeout", defaultCheckTimeoutSeconds, "Time out after this many seconds")
	allCmd.Flags().ParseErrorsWhitelist.UnknownFlags = true
	allCmd.FParseErrWhitelist.UnknownFlags = true
	return allCmd
}

// NewGuardHpaCommand returns a new instance of an `argocd app create` command
func NewGuardHpaCommand(clientOpts *argocdclient.ClientOptions) *cobra.Command {
	var dryRun bool
	var timeout uint
	var command = &cobra.Command{
		Use:   "hpa <App Name>",
		Short: "Check HPA and specs of objects refereneced by HPA",
	}

	command.Run = func(c *cobra.Command, args []string) {
		var appName string
		if len(args) > 1 {
			for i := range args {
				if strings.HasPrefix(args[i], "--") {
					continue
				} else {
					appName = args[i]
					break
				}
			}
			if appName == "" {
				c.HelpFunc()(c, args)
				os.Exit(1)
			}
		} else if len(args) == 1 {
			appName = args[0]
		} else {
			c.HelpFunc()(c, args)
			os.Exit(1)
		}

		clientOpts.Insecure = true
		apiClient := argocdclient.NewClientOrDie(clientOpts)
		conn, appIf := apiClient.NewApplicationClientOrDie()
		defer util.Close(conn)
		ctx := context.Background()
		_, err := appIf.Get(context.Background(), &application.ApplicationQuery{Name: &appName, Refresh: getRefreshType(true, false)})
		if err != nil {
			log.Error(err)
			return
		}
		resourceDiffs, err := appIf.ManagedResources(ctx, &application.ResourcesQuery{ApplicationName: &appName})
		if err != nil {
			log.Error(err)
			return
		}

		_, resourceNames, resources, statusCode := verifyHpa(resourceDiffs.Items)

		if statusCode != 0 {
			return
		}

		//Apply patches
		applyLastAppliedConfigPatch(ctx, appIf, appName, resourceNames, resources, dryRun)
	}
	command.Flags().BoolVar(&dryRun, "dryRun", false, "If true, it won't make any changes.")
	command.Flags().UintVar(&timeout, "timeout", defaultCheckTimeoutSeconds, "Time out after this many seconds")
	command.Flags().ParseErrorsWhitelist.UnknownFlags = true
	command.PersistentFlags().ParseErrorsWhitelist.UnknownFlags = true
	command.FParseErrWhitelist.UnknownFlags = true

	return command
}

//Call ArgoCD patch to apply "kubectl.kubernetes.io/last-applied-configuration" patch on DeploymentSpec and RolloutSpec
func applyLastAppliedConfigPatch(ctx context.Context, appIf application.ApplicationServiceClient, appName string, resourceNames []string, resources map[string]*argoappv1.ResourceDiff, dryRun bool) {
	if len(resources) != 0 { //
		//The remain deployments or rollouts need to be applied
		for i := range resourceNames {
			resourceName := resourceNames[i]
			if resourceName != "" {
				var resource = resources[resourceName]
				var namespace = resource.Namespace
				var liveObj, _ = resource.LiveObject()
				liveObjCopy := liveObj.DeepCopy()
				if namespace == "" {
					namespace = liveObj.GetNamespace()
				}

				var lastAppliedConfig = ""

				metadataObj := liveObjCopy.Object["metadata"]
				if metadataObj != nil && reflect.TypeOf(metadataObj).String() == "map[string]interface {}" {
					metadata := metadataObj.(map[string]interface{})
					annoObj := metadata["annotations"]
					if annoObj != nil && reflect.TypeOf(annoObj).String() == "map[string]interface {}" {
						anno := annoObj.(map[string]interface{})
						lastAppliedConfig = anno["kubectl.kubernetes.io/last-applied-configuration"].(string)
						delete(anno, "kubectl.kubernetes.io/last-applied-configuration")
					}
				}

				var lastAppliedConfigObj *unstructured.Unstructured = nil
				if lastAppliedConfig != "" { //Use the last-applied-configuration instead of liveObject
					lastAppliedConfigObj = &unstructured.Unstructured{}
					err := json.Unmarshal([]byte(lastAppliedConfig), lastAppliedConfigObj)
					if err != nil {
						log.Errorf("Not able to unmarshal last-applied-configuration %s %v", lastAppliedConfig, err)
					}
					liveObjCopy = lastAppliedConfigObj
				}

				specObj := liveObjCopy.Object["spec"]
				if specObj != nil && reflect.TypeOf(specObj).String() == "map[string]interface {}" {
					spec := specObj.(map[string]interface{})
					delete(spec, "replicas")
				}
				delete(liveObjCopy.Object, "status")

				bytes, err := json.Marshal(liveObjCopy)
				if err != nil {
					log.Errorf("Not able to marshal %s Spec", liveObjCopy.GetKind())
					os.Exit(1)
				}

				newPatch := make(map[string]interface{})
				newMetadata := make(map[string]interface{})
				newPatch["metadata"] = newMetadata
				newAnnotations := make(map[string]interface{})
				newMetadata["annotations"] = newAnnotations
				newAnnotations["kubectl.kubernetes.io/last-applied-configuration"] = string(bytes)

				//For debug
				//var tmpFileName = "/tmp/" + resourceName + strconv.FormatInt(time.Now().Unix(), 10) + ".yaml"
				//f, err := os.Create(tmpFileName)
				//if err != nil {
				//	log.Errorf("Not able to create temp file:%s", tmpFileName)
				//	os.Exit(1)
				//}
				////log.Infof("Writing current resource to yaml file:%s", tmpFileName)
				yamlBytes, err := json.Marshal(newPatch)
				//f.Write(yamlBytes)
				//f.Sync()
				//f.Close()

				//var fileNames = make([]string, 1)
				//fileNames[0] = tmpFileName

				if !dryRun {
					_, err = appIf.PatchResource(ctx, &application.ApplicationResourcePatchRequest{
						Name:         &appName,
						Namespace:    namespace,
						ResourceName: resourceName,
						Version:      liveObjCopy.GetAPIVersion(),
						Group:        resource.Group,
						Kind:         resource.Kind,
						Patch:        string(yamlBytes),
						PatchType:    "application/merge-patch+json",
					})
					if err != nil {
						log.Errorf("Patching annoation 'kubectl.kubernetes.io/last-applied-configuration' on resource: %s, error:%v", resourceName, err)
					} else {
						log.Infof("Resource '%s' patched on 'kubectl.kubernetes.io/last-applied-configuration'", resourceName)
					}
				} else {
					log.Infof("DryRun on resource '%s' patch of 'kubectl.kubernetes.io/last-applied-configuration'", resourceName)
				}
			}
		}
	}
}

// hpaReferencesObjects finds all the resources that the HPA spec references
func hpaReferencesObjects(resourceDiffs []*argoappv1.ResourceDiff) ([]*unstructured.Unstructured, []string, map[string]*argoappv1.ResourceDiff) {
	hpaObjects := make([]*unstructured.Unstructured, 0)
	resourceNames := make([]string, 0)
	resources := make(map[string]*argoappv1.ResourceDiff)

	for i := range resourceDiffs {
		obj := resourceDiffs[i]
		if obj.Kind == "HorizontalPodAutoscaler" && obj.Group == "autoscaling" {
			targetObject, error := obj.TargetObject()
			if error == nil && targetObject != nil {
				specObj := targetObject.Object["spec"]
				if specObj != nil && reflect.TypeOf(specObj).String() == "map[string]interface {}" {
					spec := specObj.(map[string]interface{})
					scaleTargetRefObj := spec["scaleTargetRef"]
					if scaleTargetRefObj != nil && reflect.TypeOf(scaleTargetRefObj).String() == "map[string]interface {}" {
						scaleTargetRef := scaleTargetRefObj.(map[string]interface{})
						if scaleTargetRef["kind"] == "Deployment" || scaleTargetRef["kind"] == "Rollout" {
							copy := targetObject.DeepCopy()
							hpaObjects = append(hpaObjects, copy)
							var resourceName = scaleTargetRef["name"].(string)
							resourceNames = append(resourceNames, resourceName)

							log.Infof("The HorizontalPodAutoscaler:%s is associated with %s:%s", targetObject.GetName(), scaleTargetRef["kind"], resourceName)
						}
					}
				}
			}
		} else if obj.Kind == "Deployment" {
			resources[obj.Name] = obj.DeepCopy()
		} else if obj.Kind == "Rollout" && obj.Group == "argoproj.io" {
			resources[obj.Name] = obj.DeepCopy()
		}
	}
	return hpaObjects, resourceNames, resources
}

const defaultCheckTimeoutSeconds = 0

// NewGuardIngressCommand is to enforce Deployment object has "PodReadinessCondition for ALB-Ingress" when target-type is "ip" in Ingress
func NewGuardIngressCommand(clientOpts *argocdclient.ClientOptions) *cobra.Command {
	var dryRun bool
	var timeout uint
	var command = &cobra.Command{
		Use:   "ingress <App Name>",
		Short: "Check Ingress and Deployment",
	}

	command.Run = func(c *cobra.Command, args []string) {
		var appName string
		if len(args) > 1 {
			for i := range args {
				if strings.HasPrefix(args[i], "--") {
					continue
				} else {
					appName = args[i]
					break
				}
			}
			if appName == "" {
				c.HelpFunc()(c, args)
				os.Exit(1)
			}
		} else if len(args) == 1 {
			appName = args[0]
		} else {
			c.HelpFunc()(c, args)
			os.Exit(1)
		}

		clientOpts.Insecure = true
		apiClient := argocdclient.NewClientOrDie(clientOpts)
		conn, appIf := apiClient.NewApplicationClientOrDie()
		defer util.Close(conn)
		ctx := context.Background()
		_, err := appIf.Get(context.Background(), &application.ApplicationQuery{Name: &appName, Refresh: getRefreshType(true, false)})
		if err != nil {
			log.Error(err)
			return
		}
		resourceDiffs, err := appIf.ManagedResources(ctx, &application.ResourcesQuery{ApplicationName: &appName})
		if err != nil {
			log.Error(err)
			return
		}

		statusCode := verifyIngress(resourceDiffs.Items)

		if statusCode != 0 {
			return
		}
	}
	command.Flags().BoolVar(&dryRun, "dryRun", false, "If true, it won't make any changes.")
	command.Flags().UintVar(&timeout, "timeout", defaultCheckTimeoutSeconds, "Time out after this many seconds")
	command.Flags().ParseErrorsWhitelist.UnknownFlags = true
	command.PersistentFlags().ParseErrorsWhitelist.UnknownFlags = true
	command.FParseErrWhitelist.UnknownFlags = true

	return command
}

func verifyIngress(resourceDiffs []*argoappv1.ResourceDiff) int {
	ingresses, resources := ingressAndDeployment(resourceDiffs)
	if len(ingresses) == 0 {
		log.Infof("No Ingress found, good to pass through")
		return 0
	}

	// Ingress Name -->  True or False
	podReadinessGateEnabled := make(map[string]bool)
	ingressMap := make(map[string]*unstructured.Unstructured)

	// Set the mapping to false if the ingress has annotation alb.ingress.kubernetes.io/target-type=ip
	for i := range ingresses {
		ingress := ingresses[i]
		ingressName := ingress.GetName()
		var metadataObj = ingress.Object["metadata"]
		if metadataObj != nil && reflect.TypeOf(metadataObj).String() == "map[string]interface {}" {
			metadata := metadataObj.(map[string]interface{})
			if metadata["annotations"] != nil && reflect.TypeOf(metadata["annotations"]).String() == "map[string]interface {}" {
				annotations := metadata["annotations"].(map[string]interface{})

				var lastAppliedConfiguration = annotations["alb.ingress.kubernetes.io/target-type"]
				if lastAppliedConfiguration == "ip" {
					podReadinessGateEnabled[ingressName] = false
				}
			}
		}
		ingressMap[ingressName] = ingress
	}

	if len(podReadinessGateEnabled) == 0 { //No ingress object,
		log.Infof("No Ingress has annotation 'alb.ingress.kubernetes.io/target-type=ip', good to pass through")
		return 0
	}

	// Each Ingress should have at least one PodReadinessGate points to it
	// Resources could be Deployment or Rollout
	for i := range resources {
		resource := resources[i]

		resourceTarget, error := resource.TargetObject()
		if error != nil || resourceTarget == nil {
			log.Errorf("The target object has error %v", error)
			return 200
		}

		specObj := resourceTarget.Object["spec"]
		if specObj != nil && reflect.TypeOf(specObj).String() == "map[string]interface {}" {
			spec := specObj.(map[string]interface{})

			templateObj := spec["template"]
			if templateObj != nil && reflect.TypeOf(templateObj).String() == "map[string]interface {}" {
				template := templateObj.(map[string]interface{})

				if template["spec"] != nil && reflect.TypeOf(template["spec"]).String() == "map[string]interface {}" {
					templateSpec := template["spec"].(map[string]interface{})

					if templateSpec["readinessGates"] != nil && reflect.TypeOf(templateSpec["readinessGates"]).String() == "[]interface {}" {

						readinessGates := templateSpec["readinessGates"].([]interface{})

						if len(readinessGates) > 0 {
							for j := range readinessGates {
								conditionObj := readinessGates[j]

								var conditionType = ""
								if reflect.TypeOf(conditionObj).String() == "map[string]interface {}" {
									condition := conditionObj.(map[string]interface{})
									if condition["conditionType"] != nil {
										conditionType = condition["conditionType"].(string)
									}
								} else if reflect.TypeOf(conditionObj).String() == "map[string]string" {
									condition := conditionObj.(map[string]string)
									if condition["conditionType"] != "" {
										conditionType = condition["conditionType"]
									}
								}

								if strings.HasPrefix(conditionType, "target-health.alb.ingress.k8s.aws/") {
									var l = len("target-health.alb.ingress.k8s.aws/")
									var suffix = conditionType[l:]
									if len(suffix) > 0 {
										if len(suffix) > 63 { //Bug in alb-ingress-controller https://github.intuit.com/kubernetes/arktika/issues/935#issuecomment-1107371
											// https://github.com/kubernetes-sigs/aws-alb-ingress-controller/issues/1217
											log.Errorf("The pod readiness conditionType '%s' is more than 63 characters which a limitation from k8s, please use static conditionType 'load-balancer-tg-ready' instead", suffix)
											os.Exit(306)
											return 306
										}
										if suffix == "load-balancer-any-tg-ready" || suffix == "load-balancer-all-tg-ready" { // In this case, cd-guard will allow all Ingress passed
											podReadinessGateEnabled["*"] = true
										} else {
											var array = strings.Split(suffix, "_")
											if len(array) != 3 {
												log.Errorf("The pod readiness condition %s doesn't have 3 parts separated with '_', the right syntax is 'INGRESS_SERVICE_PORT'", conditionType)
												os.Exit(301)
												return 301
											} else {
												ingressName := array[0]
												if ingress, ok := ingressMap[ingressName]; ok {
													if _, hasKey := podReadinessGateEnabled[ingressName]; hasKey {
														//Check whether the service and port are existing.
														if goodStatus := verifyIngressServicePort(ingress, ingressName, array[1], array[2]); goodStatus {
															podReadinessGateEnabled[ingressName] = true
														} else {
															log.Errorf("The service name or port [%s:%s] deson't exist in ingress %s", array[1], array[2], ingressName)
															os.Exit(305)
															return 305
														}
													} else { //Pod Readiness Condition points to an Ingress doesn't have target-type=ip annotation
														log.Errorf("You have a pod readiness condition, but the Ingress %s doesn't have an annotation 'alb.ingress.kubernetes.io/target-type' with value 'ip'", ingressName)
														os.Exit(302)
														return 302
													}
												} else { //Pod Readiness Condition points to a non-exists Ingress
													log.Errorf("You have a pod readiness condition, but the Ingress %s doesn't exist", ingressName)
													os.Exit(304)
													return 304
												}
											}
										}
									} else {
										log.Errorf("The pod readiness condition %s doesn't point to the right INGRESS_SERVICE_PORT", conditionType)
										os.Exit(300)
										return 300
									}
								}
							}
						}
					}
				}
			}
		}
	}

	for ingressName, gateEnabled := range podReadinessGateEnabled {
		if !gateEnabled {
			if !(podReadinessGateEnabled["*"]) { //If there is static conditionType, we don't check whether the pods belongs to Ingress, instead just let the Ingress pass through.
				log.Errorf("Ingress '%s' with flat network, but no pod enables PodReadinessGate, please refer to this doc https://github.intuit.com/kubernetes/modern-saas-docs/blob/master/docs/developer/msaas_resiliency_iks2.md", ingressName)
				os.Exit(500)
				return 500
			}
		}
	}

	return 0
}

func verifyIngressServicePort(ingress *unstructured.Unstructured, ingressName string, serviceName string, port string) bool {
	specObj := ingress.Object["spec"]
	if specObj != nil && reflect.TypeOf(specObj).String() == "map[string]interface {}" {
		spec := specObj.(map[string]interface{})
		// 1. Single Service Ingress https://kubernetes.io/docs/concepts/services-networking/ingress/#single-service-ingress
		/*
		  backend:
		    serviceName: testsvc
		    servicePort: 80
		*/
		backendObj := spec["backend"]
		if backendObj != nil && reflect.TypeOf(backendObj).String() == "map[string]interface {}" {
			backend := backendObj.(map[string]interface{})
			if backend["serviceName"] != nil && backend["servicePort"] != nil {
				return strings.EqualFold(backend["serviceName"].(string), serviceName) && string(backend["servicePort"].(int)) == port
			}
		}

		// 2. Simple fanout https://kubernetes.io/docs/concepts/services-networking/ingress/#simple-fanout
		// spec/rules[]/http/paths[]/backend
		rulesObj := spec["rules"]
		if rulesObj != nil && reflect.TypeOf(rulesObj).String() == "[]interface {}" {
			rules := rulesObj.([]interface{})
			for _, ruleObj := range rules {
				if reflect.TypeOf(ruleObj).String() == "map[string]interface {}" {
					rule := ruleObj.(map[string]interface{})
					if rule["http"] != nil && reflect.TypeOf(rule["http"]).String() == "map[string]interface {}" {
						http := rule["http"].(map[string]interface{})
						pathsObj := http["paths"]
						if pathsObj != nil && reflect.TypeOf(pathsObj).String() == "[]interface {}" {
							paths := pathsObj.([]interface{})
							for _, pathObj := range paths {
								if reflect.TypeOf(pathObj).String() == "map[string]interface {}" {
									path := pathObj.(map[string]interface{})

									if path["backend"] != nil && reflect.TypeOf(path["backend"]).String() == "map[string]interface {}" {
										backend := path["backend"].(map[string]interface{})
										if backend["serviceName"] != nil && backend["servicePort"] != nil {
											if strings.EqualFold(backend["serviceName"].(string), serviceName) && strconv.FormatInt(backend["servicePort"].(int64), 10) == port {
												return true
											}
										}
									}
								}
							}
						}
					}
				}
			}
		}
	}

	return false
}

func ingressAndDeployment(resourceDiffs []*argoappv1.ResourceDiff) ([]*unstructured.Unstructured, map[string]*argoappv1.ResourceDiff) {
	ingressObjects := make([]*unstructured.Unstructured, 0)
	deploymentOrRollout := make(map[string]*argoappv1.ResourceDiff)

	for i := range resourceDiffs {
		obj := resourceDiffs[i]
		if obj.Kind == "Ingress" {
			targetObject, error := obj.TargetObject()
			if error == nil && targetObject != nil {
				copy := targetObject.DeepCopy()
				ingressObjects = append(ingressObjects, copy)
			}
		} else if obj.Kind == "Deployment" {
			deploymentOrRollout[obj.Name] = obj.DeepCopy()
		} else if obj.Kind == "Rollout" && obj.Group == "argoproj.io" {
			deploymentOrRollout[obj.Name] = obj.DeepCopy()
		}
	}
	return ingressObjects, deploymentOrRollout
}
