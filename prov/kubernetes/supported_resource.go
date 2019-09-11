// Copyright 2019 Bull S.A.S. Atos Technologies - Bull, Rue Jean Jaures, B.P.68, 78340, Les Clayes-sous-Bois, France.
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//      http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

package kubernetes

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/pkg/errors"
	appsv1 "k8s.io/api/apps/v1beta1"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/api/extensions/v1beta1"
	k8serrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/client-go/kubernetes"
)

//Interface to implement for new supported objects in K8s
type yorcK8sObject interface {
	// Operations for yorc kubernetes objects
	createResource(ctx context.Context, deploymentID string, clientset kubernetes.Interface, namespace string) error
	deleteResource(ctx context.Context, deploymentID string, clientset kubernetes.Interface, namespace string) error
	scaleResource(ctx context.Context, deploymentID string, clientset kubernetes.Interface, namespace string, expectedInstances int32) error
	// Return a boolean telling if the resource is correctly deployed on K8s and error message if necessary
	isSuccessfullyDeployed(ctx context.Context, deploymentID string, clientset kubernetes.Interface) (bool, error)
	// Return if the specified resource is correctly deleted
	isSuccessfullyDeleted(ctx context.Context, deploymentID string, clientset kubernetes.Interface) (bool, error)
	// unmarshal the resourceSpec into struct
	unmarshalResource(ctx context.Context, e *execution, deploymentID string, clientset kubernetes.Interface, rSpec string) error
	streamLogs(ctx context.Context, deploymentID string, clientset kubernetes.Interface)
	getObjectMeta() metav1.ObjectMeta
	// Implem of the stringer interface
	fmt.Stringer

	getObjectRuntime() runtime.Object
}

// Supported k8s resources
type yorcK8sPersistentVolumeClaim corev1.PersistentVolumeClaim
type yorcK8sService corev1.Service
type yorcK8sDeployment v1beta1.Deployment
type yorcK8sStatefulSet appsv1.StatefulSet

/*
	----------------------------------------------
	| 			PersistentVolumeClaim			 |
	----------------------------------------------
*/
//Implem of yorcK8sObject interface for PersistentVolumeClaim
func (yorcPVC *yorcK8sPersistentVolumeClaim) unmarshalResource(ctx context.Context, e *execution, deploymentID string, clientset kubernetes.Interface, rSpec string) error {
	return json.Unmarshal([]byte(rSpec), &yorcPVC)
}

func (yorcPVC *yorcK8sPersistentVolumeClaim) getObjectMeta() metav1.ObjectMeta {
	return yorcPVC.ObjectMeta
}

func (yorcPVC *yorcK8sPersistentVolumeClaim) createResource(ctx context.Context, deploymentID string, clientset kubernetes.Interface, namespace string) error {
	pvc := corev1.PersistentVolumeClaim(*yorcPVC)
	_, err := clientset.CoreV1().PersistentVolumeClaims(namespace).Create(&pvc)
	return err
}

func (yorcPVC *yorcK8sPersistentVolumeClaim) deleteResource(ctx context.Context, deploymentID string, clientset kubernetes.Interface, namespace string) error {
	pvc := corev1.PersistentVolumeClaim(*yorcPVC)
	return clientset.CoreV1().PersistentVolumeClaims(namespace).Delete(pvc.Name, nil)
}

func (yorcPVC *yorcK8sPersistentVolumeClaim) scaleResource(ctx context.Context, deploymentID string, clientset kubernetes.Interface, namespace string, expectedInstances int32) error {
	return errors.New("Scale operation is not supported by PersistentVolumeClaims")
}

func (yorcPVC *yorcK8sPersistentVolumeClaim) isSuccessfullyDeployed(ctx context.Context, deploymentID string, clientset kubernetes.Interface) (bool, error) {
	pvc, err := clientset.CoreV1().PersistentVolumeClaims(yorcPVC.Namespace).Get(yorcPVC.Name, metav1.GetOptions{})
	if err != nil {
		return false, err
	}
	if pvc == nil {
		return false, nil
	}
	if pvc.Status.Phase == corev1.ClaimBound {
		return true, nil
	}
	return false, nil
}

func (yorcPVC *yorcK8sPersistentVolumeClaim) isSuccessfullyDeleted(ctx context.Context, deploymentID string, clientset kubernetes.Interface) (bool, error) {
	_, err := clientset.CoreV1().PersistentVolumeClaims(yorcPVC.Namespace).Get(yorcPVC.Name, metav1.GetOptions{})
	if err != nil {
		if k8serrors.IsNotFound(err) {
			return true, nil
		}
		return false, err
	}
	return false, nil
}

func (yorcPVC *yorcK8sPersistentVolumeClaim) String() string {
	return "YorcPersistentVolumeClaim"
}

func (yorcPVC *yorcK8sPersistentVolumeClaim) getObjectRuntime() runtime.Object {
	pvc := corev1.PersistentVolumeClaim(*yorcPVC)
	return &pvc
}

func (yorcPVC *yorcK8sPersistentVolumeClaim) streamLogs(ctx context.Context, deploymentID string, clientset kubernetes.Interface) {
	return
}

/*
	----------------------------------------------
	| 				Deployment					 |
	----------------------------------------------
*/
func (yorcDep *yorcK8sDeployment) unmarshalResource(ctx context.Context, e *execution, deploymentID string, clientset kubernetes.Interface, rSpec string) error {
	json.Unmarshal([]byte(rSpec), &yorcDep)
	ns, _ := getNamespace(e.deploymentID, yorcDep.ObjectMeta)
	rSpec, err := e.replaceServiceIPInDeploymentSpec(ctx, clientset, ns, rSpec)
	if err != nil {
		return err
	}

	return json.Unmarshal([]byte(rSpec), &yorcDep)
}

func (yorcDep *yorcK8sDeployment) getObjectMeta() metav1.ObjectMeta {
	return yorcDep.ObjectMeta
}

func (yorcDep *yorcK8sDeployment) createResource(ctx context.Context, deploymentID string, clientset kubernetes.Interface, namespace string) error {
	deploy := v1beta1.Deployment(*yorcDep)
	// TODO: replace service_lookup
	_, err := clientset.ExtensionsV1beta1().Deployments(namespace).Create(&deploy)
	return err
}

func (yorcDep *yorcK8sDeployment) deleteResource(ctx context.Context, deploymentID string, clientset kubernetes.Interface, namespace string) error {
	deploy := v1beta1.Deployment(*yorcDep)
	deletePolicy := metav1.DeletePropagationForeground
	var gracePeriod int64 = 5
	return clientset.ExtensionsV1beta1().Deployments(namespace).Delete(deploy.Name, &metav1.DeleteOptions{
		GracePeriodSeconds: &gracePeriod, PropagationPolicy: &deletePolicy})
}

func (yorcDep *yorcK8sDeployment) scaleResource(ctx context.Context, deploymentID string, clientset kubernetes.Interface, namespace string, expectedInstances int32) error {
	deploy := v1beta1.Deployment(*yorcDep)
	deploy.Spec.Replicas = &expectedInstances

	_, err := clientset.ExtensionsV1beta1().Deployments(namespace).Update(&deploy)
	if err != nil {
		return errors.Wrap(err, "failed to update kubernetes deployment for scaling")
	}
	return errors.New("Scale operation not yet supported")
}

func (yorcDep *yorcK8sDeployment) isSuccessfullyDeployed(ctx context.Context, deploymentID string, clientset kubernetes.Interface) (bool, error) {
	dep, err := clientset.ExtensionsV1beta1().Deployments(yorcDep.Namespace).Get(yorcDep.Name, metav1.GetOptions{})
	if err != nil {
		return false, err
	}
	if dep == nil {
		return false, nil
	}
	if dep.Status.AvailableReplicas == *yorcDep.Spec.Replicas {
		return true, nil
	}
	/*  TODO:manage this
	if failed, msg := isDeploymentFailed(clientset, yorcDep); failed {
		events.WithContextOptionalFields(ctx).NewLogEntry(events.LogLevelERROR, deploymentID).Registerf("Kubernetes deployment %q failed: %s", yorcDep.Name, msg)
		return false, errors.Errorf("Kubernetes deployment %q: %s", yorcDep.Name, msg)
	}
	*/
	return false, nil
}

func (yorcDep *yorcK8sDeployment) isSuccessfullyDeleted(ctx context.Context, deploymentID string, clientset kubernetes.Interface) (bool, error) {
	_, err := clientset.ExtensionsV1beta1().Deployments(yorcDep.Namespace).Get(yorcDep.Name, metav1.GetOptions{})
	if err != nil {
		if k8serrors.IsNotFound(err) {
			return true, nil
		}
		return false, err
	}
	return false, nil
}

func (yorcDep *yorcK8sDeployment) String() string {
	return "YorcDeployment"
}

func (yorcDep *yorcK8sDeployment) getObjectRuntime() runtime.Object {
	deploy := v1beta1.Deployment(*yorcDep)
	return &deploy
}

func (yorcDep *yorcK8sDeployment) streamLogs(ctx context.Context, deploymentID string, clientset kubernetes.Interface) {
	deploy := v1beta1.Deployment(*yorcDep)
	streamDeploymentLogs(ctx, deploymentID, clientset, &deploy)
}

/*
	----------------------------------------------
	| 				StatefulSet					 |
	----------------------------------------------
*/
func (yorcSts *yorcK8sStatefulSet) unmarshalResource(ctx context.Context, e *execution, deploymentID string, clientset kubernetes.Interface, rSpec string) error {
	return json.Unmarshal([]byte(rSpec), &yorcSts)
}

func (yorcSts *yorcK8sStatefulSet) getObjectMeta() metav1.ObjectMeta {
	return yorcSts.ObjectMeta
}

func (yorcSts *yorcK8sStatefulSet) createResource(ctx context.Context, deploymentID string, clientset kubernetes.Interface, namespace string) error {
	sts := appsv1.StatefulSet(*yorcSts)
	_, err := clientset.AppsV1beta1().StatefulSets(namespace).Create(&sts)
	return err
}

func (yorcSts *yorcK8sStatefulSet) deleteResource(ctx context.Context, deploymentID string, clientset kubernetes.Interface, namespace string) error {
	sts := appsv1.StatefulSet(*yorcSts)
	return clientset.AppsV1beta1().StatefulSets(namespace).Delete(sts.Name, nil)
}

func (yorcSts *yorcK8sStatefulSet) scaleResource(ctx context.Context, deploymentID string, clientset kubernetes.Interface, namespace string, expectedInstances int32) error {
	return errors.New("Scale operation not yet supported")
}

func (yorcSts *yorcK8sStatefulSet) isSuccessfullyDeployed(ctx context.Context, deploymentID string, clientset kubernetes.Interface) (bool, error) {
	stfs, err := clientset.AppsV1beta1().StatefulSets(yorcSts.Namespace).Get(yorcSts.Name, metav1.GetOptions{})
	if err != nil {
		return false, err
	}
	if stfs == nil {
		return false, nil
	}
	if stfs.Status.ReadyReplicas == *yorcSts.Spec.Replicas {
		return true, nil
	}
	return false, nil
}

func (yorcSts *yorcK8sStatefulSet) isSuccessfullyDeleted(ctx context.Context, deploymentID string, clientset kubernetes.Interface) (bool, error) {
	_, err := clientset.AppsV1beta1().StatefulSets(yorcSts.Namespace).Get(yorcSts.Name, metav1.GetOptions{})
	if err != nil {
		if k8serrors.IsNotFound(err) {
			return true, nil
		}
		return false, err
	}
	return false, nil
}

func (yorcSts *yorcK8sStatefulSet) String() string {
	return "YorcStatefulSet"
}

func (yorcSts *yorcK8sStatefulSet) getObjectRuntime() runtime.Object {
	sts := appsv1.StatefulSet(*yorcSts)
	return &sts
}

func (yorcSts *yorcK8sStatefulSet) streamLogs(ctx context.Context, deploymentID string, clientset kubernetes.Interface) {
	// TODO : stream logs for this controller
	return
}

/*
	----------------------------------------------
	| 					Service					 |
	----------------------------------------------
*/
func (yorcSvc *yorcK8sService) unmarshalResource(ctx context.Context, e *execution, deploymentID string, clientset kubernetes.Interface, rSpec string) error {
	return json.Unmarshal([]byte(rSpec), &yorcSvc)
}

func (yorcSvc *yorcK8sService) getObjectMeta() metav1.ObjectMeta {
	return yorcSvc.ObjectMeta
}

func (yorcSvc *yorcK8sService) createResource(ctx context.Context, deploymentID string, clientset kubernetes.Interface, namespace string) error {
	svc := corev1.Service(*yorcSvc)
	_, err := clientset.CoreV1().Services(namespace).Create(&svc)
	return err
}

func (yorcSvc *yorcK8sService) deleteResource(ctx context.Context, deploymentID string, clientset kubernetes.Interface, namespace string) error {
	svc := corev1.Service(*yorcSvc)
	return clientset.CoreV1().Services(namespace).Delete(svc.Name, nil)
}

func (yorcSvc *yorcK8sService) scaleResource(ctx context.Context, deploymentID string, clientset kubernetes.Interface, namespace string, expectedInstances int32) error {
	return errors.New("Scale operation not supported by Services")
}

func (yorcSvc *yorcK8sService) isSuccessfullyDeployed(ctx context.Context, deploymentID string, clientset kubernetes.Interface) (bool, error) {
	_, err := clientset.CoreV1().Services(yorcSvc.Namespace).Get(yorcSvc.Name, metav1.GetOptions{})
	if err != nil {
		return false, err
	}
	return true, nil
}

func (yorcSvc *yorcK8sService) isSuccessfullyDeleted(ctx context.Context, deploymentID string, clientset kubernetes.Interface) (bool, error) {
	_, err := clientset.CoreV1().Services(yorcSvc.Namespace).Get(yorcSvc.Name, metav1.GetOptions{})
	if err != nil {
		if k8serrors.IsNotFound(err) {
			return true, nil
		}
		return false, err
	}
	return false, nil
}

func (yorcSvc *yorcK8sService) String() string {
	return "YorcService"
}

func (yorcSvc *yorcK8sService) getObjectRuntime() runtime.Object {
	svc := corev1.Service(*yorcSvc)
	return &svc
}

func (yorcSvc *yorcK8sService) streamLogs(ctx context.Context, deploymentID string, clientset kubernetes.Interface) {
	return
}
