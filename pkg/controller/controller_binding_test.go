/*
Copyright 2017 The Kubernetes Authors.

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

package controller

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"reflect"
	"strings"
	"testing"
	"time"

	scmeta "github.com/kubernetes-incubator/service-catalog/pkg/api/meta"
	"github.com/kubernetes-incubator/service-catalog/pkg/apis/servicecatalog/v1alpha1"
	osb "github.com/pmorie/go-open-service-broker-client/v2"
	fakeosb "github.com/pmorie/go-open-service-broker-client/v2/fake"
	apiv1 "k8s.io/client-go/pkg/api/v1"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/util/diff"
	utilfeature "k8s.io/apiserver/pkg/util/feature"

	"github.com/kubernetes-incubator/service-catalog/pkg/api"
	scfeatures "github.com/kubernetes-incubator/service-catalog/pkg/features"
	"k8s.io/client-go/pkg/api/v1"
	clientgotesting "k8s.io/client-go/testing"
)

// TestReconcileBindingNonExistingInstance tests reconcileBinding to ensure a
// binding fails as expected when an instance to bind to doesn't exist.
func TestReconcileServiceInstanceCredentialNonExistingServiceInstance(t *testing.T) {
	_, fakeCatalogClient, fakeClusterServiceBrokerClient, testController, _ := newTestController(t, noFakeActions())

	binding := &v1alpha1.ServiceInstanceCredential{
		ObjectMeta: metav1.ObjectMeta{
			Name:       testServiceInstanceCredentialName,
			Generation: 1,
		},
		Spec: v1alpha1.ServiceInstanceCredentialSpec{
			ServiceInstanceRef: v1.LocalObjectReference{Name: "nothere"},
			ExternalID:         bindingGUID,
		},
	}

	err := testController.reconcileServiceInstanceCredential(binding)
	if err == nil {
		t.Fatal("binding nothere was found and it should not be found")
	}

	brokerActions := fakeClusterServiceBrokerClient.Actions()
	assertNumberOfClusterServiceBrokerActions(t, brokerActions, 0)

	actions := fakeCatalogClient.Actions()
	assertNumberOfActions(t, actions, 1)

	// There should only be one action that says it failed because no such instance exists.
	updateAction := actions[0].(clientgotesting.UpdateAction)
	if e, a := "update", updateAction.GetVerb(); e != a {
		t.Fatalf("Unexpected verb on actions[0]; expected %v, got %v", e, a)
	}
	updatedServiceInstanceCredential := assertUpdateStatus(t, actions[0], binding)
	assertServiceInstanceCredentialErrorBeforeRequest(t, updatedServiceInstanceCredential, errorNonexistentServiceInstanceReason, binding)
	assertServiceInstanceCredentialOrphanMitigationSet(t, updatedServiceInstanceCredential, false)

	events := getRecordedEvents(testController)
	assertNumEvents(t, events, 1)

	expectedEvent := apiv1.EventTypeWarning + " " + errorNonexistentServiceInstanceReason + " " + "ServiceInstanceCredential \"/test-binding\" references a non-existent ServiceInstance \"/nothere\""
	if e, a := expectedEvent, events[0]; e != a {
		t.Fatalf("Received unexpected event: %v", a)
	}
}

// TestReconcileServiceInstanceCredentialUnresolvedServiceClassReference
// tests reconcileBinding to ensure a binding fails when a ServiceClassRef has not been resolved.
func TestReconcileServiceInstanceCredentialUnresolvedServiceClassReference(t *testing.T) {
	_, fakeCatalogClient, fakeClusterServiceBrokerClient, testController, sharedInformers := newTestController(t, noFakeActions())

	sharedInformers.ClusterServiceBrokers().Informer().GetStore().Add(getTestClusterServiceBroker())
	sharedInformers.ServiceClasses().Informer().GetStore().Add(getTestServiceClass())
	instance := &v1alpha1.ServiceInstance{
		ObjectMeta: metav1.ObjectMeta{Name: testServiceInstanceName, Namespace: testNamespace},
		Spec: v1alpha1.ServiceInstanceSpec{
			ExternalServiceClassName: "nothere",
			ExternalServicePlanName:  testServicePlanName,
			ExternalID:               instanceGUID,
		},
	}
	sharedInformers.ServiceInstances().Informer().GetStore().Add(instance)
	sharedInformers.ServicePlans().Informer().GetStore().Add(getTestServicePlan())

	binding := &v1alpha1.ServiceInstanceCredential{
		ObjectMeta: metav1.ObjectMeta{
			Name:       testServiceInstanceCredentialName,
			Namespace:  testNamespace,
			Generation: 1,
		},
		Spec: v1alpha1.ServiceInstanceCredentialSpec{
			ServiceInstanceRef: v1.LocalObjectReference{Name: testServiceInstanceName},
			ExternalID:         bindingGUID,
		},
	}

	err := testController.reconcileServiceInstanceCredential(binding)
	if err == nil {
		t.Fatal("serviceclassref was nil and reconcile should return an error")
	}
	if !strings.Contains(err.Error(), "not been resolved yet") {
		t.Fatalf("Did not get the expected error %q : got %q", "not been resolved yet", err)
	}

	brokerActions := fakeClusterServiceBrokerClient.Actions()
	assertNumberOfClusterServiceBrokerActions(t, brokerActions, 0)

	actions := fakeCatalogClient.Actions()
	// There are no actions.
	assertNumberOfActions(t, actions, 0)
}

// TestReconcileServiceInstanceCredentialUnresolvedServicePlanReference
// tests reconcileBinding to ensure a binding fails when a ServiceClassRef has not been resolved.
func TestReconcileServiceInstanceCredentialUnresolvedServicePlanReference(t *testing.T) {
	_, fakeCatalogClient, fakeClusterServiceBrokerClient, testController, sharedInformers := newTestController(t, noFakeActions())

	sharedInformers.ClusterServiceBrokers().Informer().GetStore().Add(getTestClusterServiceBroker())
	sharedInformers.ServiceClasses().Informer().GetStore().Add(getTestServiceClass())
	instance := &v1alpha1.ServiceInstance{
		ObjectMeta: metav1.ObjectMeta{Name: testServiceInstanceName, Namespace: testNamespace},
		Spec: v1alpha1.ServiceInstanceSpec{
			ExternalServiceClassName: "nothere",
			ExternalServicePlanName:  testServicePlanName,
			ExternalID:               instanceGUID,
			ServiceClassRef:          &v1.ObjectReference{Name: "Some Ref"},
		},
	}
	sharedInformers.ServiceInstances().Informer().GetStore().Add(instance)
	sharedInformers.ServicePlans().Informer().GetStore().Add(getTestServicePlan())

	binding := &v1alpha1.ServiceInstanceCredential{
		ObjectMeta: metav1.ObjectMeta{
			Name:       testServiceInstanceCredentialName,
			Namespace:  testNamespace,
			Generation: 1,
		},
		Spec: v1alpha1.ServiceInstanceCredentialSpec{
			ServiceInstanceRef: v1.LocalObjectReference{Name: testServiceInstanceName},
			ExternalID:         bindingGUID,
		},
	}

	err := testController.reconcileServiceInstanceCredential(binding)
	if err == nil {
		t.Fatal("serviceclass nothere was found and it should not be found")
	}

	if !strings.Contains(err.Error(), "not been resolved yet") {
		t.Fatalf("Did not get the expected error %q : got %q", "not been resolved yet", err)
	}

	brokerActions := fakeClusterServiceBrokerClient.Actions()
	assertNumberOfClusterServiceBrokerActions(t, brokerActions, 0)

	actions := fakeCatalogClient.Actions()
	// There are no actions.
	assertNumberOfActions(t, actions, 0)
}

// TestReconcileBindingNonExistingServiceClass tests reconcileBinding to ensure a
// binding fails as expected when a serviceclass does not exist.
func TestReconcileServiceInstanceCredentialNonExistingServiceClass(t *testing.T) {
	_, fakeCatalogClient, fakeClusterServiceBrokerClient, testController, sharedInformers := newTestController(t, noFakeActions())

	sharedInformers.ClusterServiceBrokers().Informer().GetStore().Add(getTestClusterServiceBroker())
	sharedInformers.ServiceClasses().Informer().GetStore().Add(getTestServiceClass())
	instance := &v1alpha1.ServiceInstance{
		ObjectMeta: metav1.ObjectMeta{Name: testServiceInstanceName, Namespace: testNamespace},
		Spec: v1alpha1.ServiceInstanceSpec{
			ExternalServiceClassName: "nothere",
			ExternalServicePlanName:  testServicePlanName,
			ExternalID:               instanceGUID,
			ServiceClassRef:          &v1.ObjectReference{Name: "nosuchclassid"},
			ServicePlanRef:           &v1.ObjectReference{Name: "nosuchplanid"},
		},
	}
	sharedInformers.ServiceInstances().Informer().GetStore().Add(instance)
	sharedInformers.ServicePlans().Informer().GetStore().Add(getTestServicePlan())

	binding := &v1alpha1.ServiceInstanceCredential{
		ObjectMeta: metav1.ObjectMeta{
			Name:       testServiceInstanceCredentialName,
			Namespace:  testNamespace,
			Generation: 1,
		},
		Spec: v1alpha1.ServiceInstanceCredentialSpec{
			ServiceInstanceRef: v1.LocalObjectReference{Name: testServiceInstanceName},
			ExternalID:         bindingGUID,
		},
	}

	err := testController.reconcileServiceInstanceCredential(binding)
	if err == nil {
		t.Fatal("serviceclass nothere was found and it should not be found")
	}

	brokerActions := fakeClusterServiceBrokerClient.Actions()
	assertNumberOfClusterServiceBrokerActions(t, brokerActions, 0)

	actions := fakeCatalogClient.Actions()
	// There is one action to update to failed status because there's
	// no such service
	assertNumberOfActions(t, actions, 1)

	// There should be one action that says it failed because no such service class.
	updatedServiceInstanceCredential := assertUpdateStatus(t, actions[0], binding)
	assertServiceInstanceCredentialReadyFalse(t, updatedServiceInstanceCredential, errorNonexistentServiceClassMessage)
	assertServiceInstanceCredentialOrphanMitigationSet(t, updatedServiceInstanceCredential, false)

	events := getRecordedEvents(testController)
	assertNumEvents(t, events, 1)

	expectedEvent := apiv1.EventTypeWarning + " " + errorNonexistentServiceClassMessage + " " + "ServiceInstanceCredential \"test-ns/test-binding\" references a non-existent ServiceClass \"nothere\""
	if e, a := expectedEvent, events[0]; e != a {
		t.Fatalf("Received unexpected event: %v", a)
	}
}

// TestReconcileBindingWithSecretConflict tests reconcileBinding to ensure a
// binding with an existing secret not owned by the bindings fails as expected.
func TestReconcileServiceInstanceCredentialWithSecretConflict(t *testing.T) {
	fakeKubeClient, fakeCatalogClient, fakeClusterServiceBrokerClient, testController, sharedInformers := newTestController(t, fakeosb.FakeClientConfiguration{
		BindReaction: &fakeosb.BindReaction{
			Response: &osb.BindResponse{
				Credentials: map[string]interface{}{
					"a": "b",
					"c": "d",
				},
			},
		},
	})

	addGetNamespaceReaction(fakeKubeClient)
	// existing Secret with nil controllerRef
	addGetSecretReaction(fakeKubeClient, &v1.Secret{
		ObjectMeta: metav1.ObjectMeta{Name: testServiceInstanceCredentialName, Namespace: testNamespace},
	})

	sharedInformers.ClusterServiceBrokers().Informer().GetStore().Add(getTestClusterServiceBroker())
	sharedInformers.ServiceClasses().Informer().GetStore().Add(getTestServiceClass())
	sharedInformers.ServiceInstances().Informer().GetStore().Add(getTestServiceInstanceWithStatus(v1alpha1.ConditionTrue))
	sharedInformers.ServicePlans().Informer().GetStore().Add(getTestServicePlan())

	binding := &v1alpha1.ServiceInstanceCredential{
		ObjectMeta: metav1.ObjectMeta{
			Name:       testServiceInstanceCredentialName,
			Namespace:  testNamespace,
			Generation: 1,
		},
		Spec: v1alpha1.ServiceInstanceCredentialSpec{
			ServiceInstanceRef: v1.LocalObjectReference{Name: testServiceInstanceName},
			ExternalID:         bindingGUID,
			SecretName:         testServiceInstanceCredentialSecretName,
		},
	}

	err := testController.reconcileServiceInstanceCredential(binding)
	if err == nil {
		t.Fatalf("a binding should fail to create a secret: %v", err)
	}

	brokerActions := fakeClusterServiceBrokerClient.Actions()
	assertNumberOfClusterServiceBrokerActions(t, brokerActions, 1)
	assertBind(t, brokerActions[0], &osb.BindRequest{
		BindingID:  bindingGUID,
		InstanceID: instanceGUID,
		ServiceID:  serviceClassGUID,
		PlanID:     planGUID,
		AppGUID:    strPtr(testNsUID),
		BindResource: &osb.BindResource{
			AppGUID: strPtr(testNsUID),
		},
	})

	actions := fakeCatalogClient.Actions()
	assertNumberOfActions(t, actions, 2)

	updatedServiceInstanceCredential := assertUpdateStatus(t, actions[0], binding).(*v1alpha1.ServiceInstanceCredential)
	assertServiceInstanceCredentialOperationInProgress(t, updatedServiceInstanceCredential, v1alpha1.ServiceInstanceCredentialOperationBind, binding)

	updatedServiceInstanceCredential = assertUpdateStatus(t, actions[1], binding).(*v1alpha1.ServiceInstanceCredential)
	assertServiceInstanceCredentialReadyFalse(t, updatedServiceInstanceCredential, errorInjectingBindResultReason)
	assertServiceInstanceCredentialCurrentOperation(t, updatedServiceInstanceCredential, v1alpha1.ServiceInstanceCredentialOperationBind)
	assertServiceInstanceCredentialOperationStartTimeSet(t, updatedServiceInstanceCredential, true)
	assertServiceInstanceCredentialReconciledGeneration(t, updatedServiceInstanceCredential, binding.Status.ReconciledGeneration)
	assertServiceInstanceCredentialInProgressPropertiesParameters(t, updatedServiceInstanceCredential, nil, "")
	// External properties are updated because the bind request with the Broker was successful
	assertServiceInstanceCredentialExternalPropertiesParameters(t, updatedServiceInstanceCredential, nil, "")
	assertServiceInstanceCredentialOrphanMitigationSet(t, updatedServiceInstanceCredential, false)

	kubeActions := fakeKubeClient.Actions()
	assertNumberOfActions(t, kubeActions, 2)

	// first action is a get on the namespace
	// second action is a get on the secret
	action := kubeActions[1].(clientgotesting.GetAction)
	if e, a := "get", action.GetVerb(); e != a {
		t.Fatalf("Unexpected verb on action; expected %v, got %v", e, a)
	}
	if e, a := "secrets", action.GetResource().Resource; e != a {
		t.Fatalf("Unexpected resource on action; expected %v, got %v", e, a)
	}

	events := getRecordedEvents(testController)
	assertNumEvents(t, events, 1)

	expectedEvent := apiv1.EventTypeWarning + " " + errorInjectingBindResultReason
	if e, a := expectedEvent, events[0]; !strings.HasPrefix(a, e) {
		t.Fatalf("Received unexpected event: %v", a)
	}
}

// TestReconcileBindingWithParameters tests reconcileBinding to ensure a
// binding with parameters will be passed to the broker properly.
func TestReconcileServiceInstanceCredentialWithParameters(t *testing.T) {
	fakeKubeClient, fakeCatalogClient, fakeClusterServiceBrokerClient, testController, sharedInformers := newTestController(t, fakeosb.FakeClientConfiguration{
		BindReaction: &fakeosb.BindReaction{
			Response: &osb.BindResponse{
				Credentials: map[string]interface{}{
					"a": "b",
					"c": "d",
				},
			},
		},
	})

	addGetNamespaceReaction(fakeKubeClient)
	addGetSecretNotFoundReaction(fakeKubeClient)

	sharedInformers.ClusterServiceBrokers().Informer().GetStore().Add(getTestClusterServiceBroker())
	sharedInformers.ServiceClasses().Informer().GetStore().Add(getTestServiceClass())
	sharedInformers.ServiceInstances().Informer().GetStore().Add(getTestServiceInstanceWithStatus(v1alpha1.ConditionTrue))
	sharedInformers.ServicePlans().Informer().GetStore().Add(getTestServicePlan())

	binding := &v1alpha1.ServiceInstanceCredential{
		ObjectMeta: metav1.ObjectMeta{
			Name:       testServiceInstanceCredentialName,
			Namespace:  testNamespace,
			Generation: 1,
		},
		Spec: v1alpha1.ServiceInstanceCredentialSpec{
			ServiceInstanceRef: v1.LocalObjectReference{Name: testServiceInstanceName},
			ExternalID:         bindingGUID,
			SecretName:         testServiceInstanceCredentialSecretName,
		},
	}

	parameters := bindingParameters{Name: "test-param"}
	parameters.Args = append(parameters.Args, "first-arg")
	parameters.Args = append(parameters.Args, "second-arg")
	b, err := json.Marshal(parameters)
	if err != nil {
		t.Fatalf("Failed to marshal parameters %v : %v", parameters, err)
	}
	binding.Spec.Parameters = &runtime.RawExtension{Raw: b}

	err = testController.reconcileServiceInstanceCredential(binding)
	if err != nil {
		t.Fatalf("a valid binding should not fail: %v", err)
	}

	brokerActions := fakeClusterServiceBrokerClient.Actions()
	assertNumberOfClusterServiceBrokerActions(t, brokerActions, 1)
	assertBind(t, brokerActions[0], &osb.BindRequest{
		BindingID:  bindingGUID,
		InstanceID: instanceGUID,
		ServiceID:  serviceClassGUID,
		PlanID:     planGUID,
		AppGUID:    strPtr(testNsUID),
		Parameters: map[string]interface{}{
			"args": []interface{}{
				"first-arg",
				"second-arg",
			},
			"name": "test-param",
		},
		BindResource: &osb.BindResource{
			AppGUID: strPtr(testNsUID),
		},
	})

	expectedParameters := map[string]interface{}{
		"args": []interface{}{
			"first-arg",
			"second-arg",
		},
		"name": "test-param",
	}
	expectedParametersChecksum, err := generateChecksumOfParameters(expectedParameters)
	if err != nil {
		t.Fatalf("Failed to generate parameters checksum: %v", err)
	}

	actions := fakeCatalogClient.Actions()
	assertNumberOfActions(t, actions, 2)

	updatedServiceInstanceCredential := assertUpdateStatus(t, actions[0], binding).(*v1alpha1.ServiceInstanceCredential)
	assertServiceInstanceCredentialOperationInProgressWithParameters(t, updatedServiceInstanceCredential, v1alpha1.ServiceInstanceCredentialOperationBind, expectedParameters, expectedParametersChecksum, binding)
	assertServiceInstanceCredentialOrphanMitigationSet(t, updatedServiceInstanceCredential, false)

	updatedServiceInstanceCredential = assertUpdateStatus(t, actions[1], binding).(*v1alpha1.ServiceInstanceCredential)
	assertServiceInstanceCredentialOperationSuccessWithParameters(t, updatedServiceInstanceCredential, v1alpha1.ServiceInstanceCredentialOperationBind, expectedParameters, expectedParametersChecksum, binding)
	assertServiceInstanceCredentialOrphanMitigationSet(t, updatedServiceInstanceCredential, false)

	kubeActions := fakeKubeClient.Actions()
	assertNumberOfActions(t, kubeActions, 3)

	// first action is a get on the namespace
	// second action is a get on the secret
	action := kubeActions[2].(clientgotesting.CreateAction)
	if e, a := "create", action.GetVerb(); e != a {
		t.Fatalf("Unexpected verb on action; expected %v, got %v", e, a)
	}
	if e, a := "secrets", action.GetResource().Resource; e != a {
		t.Fatalf("Unexpected resource on action; expected %v, got %v", e, a)
	}
	actionSecret, ok := action.GetObject().(*v1.Secret)
	if !ok {
		t.Fatal("couldn't convert secret into a v1.Secret")
	}
	controllerRef := GetControllerOf(actionSecret)
	if controllerRef == nil || controllerRef.UID != updatedServiceInstanceCredential.UID {
		t.Fatalf("Secret is not owned by the ServiceInstanceCredential: %v", controllerRef)
	}
	if !IsControlledBy(actionSecret, updatedServiceInstanceCredential) {
		t.Fatal("Secret is not owned by the ServiceInstanceCredential")
	}
	if e, a := testServiceInstanceCredentialSecretName, actionSecret.Name; e != a {
		t.Fatalf("Unexpected name of secret; expected %v, got %v", e, a)
	}
	value, ok := actionSecret.Data["a"]
	if !ok {
		t.Fatal("Didn't find secret key 'a' in created secret")
	}
	if e, a := "b", string(value); e != a {
		t.Fatalf("Unexpected value of key 'a' in created secret; expected %v got %v", e, a)
	}
	value, ok = actionSecret.Data["c"]
	if !ok {
		t.Fatal("Didn't find secret key 'a' in created secret")
	}
	if e, a := "d", string(value); e != a {
		t.Fatalf("Unexpected value of key 'c' in created secret; expected %v got %v", e, a)
	}

	events := getRecordedEvents(testController)
	assertNumEvents(t, events, 1)

	expectedEvent := apiv1.EventTypeNormal + " " + successInjectedBindResultReason + " " + successInjectedBindResultMessage
	if e, a := expectedEvent, events[0]; e != a {
		t.Fatalf("Received unexpected event: %v", a)
	}
}

// TestReconcileBindingNonbindableServiceClass tests reconcileBinding to ensure a
// binding for an instance that references a non-bindable service class and a
// non-bindable plan fails as expected.
func TestReconcileServiceInstanceCredentialNonbindableServiceClass(t *testing.T) {
	_, fakeCatalogClient, fakeClusterServiceBrokerClient, testController, sharedInformers := newTestController(t, noFakeActions())

	sharedInformers.ClusterServiceBrokers().Informer().GetStore().Add(getTestClusterServiceBroker())
	sharedInformers.ServiceClasses().Informer().GetStore().Add(getTestNonbindableServiceClass())
	sharedInformers.ServiceInstances().Informer().GetStore().Add(getTestNonbindableServiceInstance())
	sharedInformers.ServicePlans().Informer().GetStore().Add(getTestServicePlanNonbindable())

	binding := &v1alpha1.ServiceInstanceCredential{
		ObjectMeta: metav1.ObjectMeta{
			Name:       testServiceInstanceCredentialName,
			Namespace:  testNamespace,
			Generation: 1,
		},
		Spec: v1alpha1.ServiceInstanceCredentialSpec{
			ServiceInstanceRef: v1.LocalObjectReference{Name: testServiceInstanceName},
			ExternalID:         bindingGUID,
		},
	}

	err := testController.reconcileServiceInstanceCredential(binding)
	if err != nil {
		t.Fatalf("binding should fail against a non-bindable ServiceClass")
	}

	brokerActions := fakeClusterServiceBrokerClient.Actions()
	assertNumberOfClusterServiceBrokerActions(t, brokerActions, 0)

	actions := fakeCatalogClient.Actions()
	assertNumberOfActions(t, actions, 1)

	// There should only be one action that says binding was created
	updatedServiceInstanceCredential := assertUpdateStatus(t, actions[0], binding)
	assertServiceInstanceCredentialErrorBeforeRequest(t, updatedServiceInstanceCredential, errorNonbindableServiceClassReason, binding)
	assertServiceInstanceCredentialOrphanMitigationSet(t, updatedServiceInstanceCredential, false)

	events := getRecordedEvents(testController)
	assertNumEvents(t, events, 1)

	expectedEvent := apiv1.EventTypeWarning + " " + errorNonbindableServiceClassReason + ` ServiceInstanceCredential "test-ns/test-binding" references a non-bindable ServiceClass ("test-unbindable-serviceclass") and Plan ("test-unbindable-plan") combination`
	if e, a := expectedEvent, events[0]; e != a {
		t.Fatalf("Received unexpected event: %v", a)
	}
}

// TestReconcileBindingNonbindableServiceClassBindablePlan tests reconcileBinding
// to ensure a binding for an instance that references a non-bindable service
// class and a bindable plan fails as expected.
func TestReconcileServiceInstanceCredentialNonbindableServiceClassBindablePlan(t *testing.T) {
	fakeKubeClient, fakeCatalogClient, fakeClusterServiceBrokerClient, testController, sharedInformers := newTestController(t, fakeosb.FakeClientConfiguration{
		BindReaction: &fakeosb.BindReaction{
			Response: &osb.BindResponse{
				Credentials: map[string]interface{}{
					"a": "b",
					"c": "d",
				},
			},
		},
	})

	addGetNamespaceReaction(fakeKubeClient)
	addGetSecretNotFoundReaction(fakeKubeClient)

	sharedInformers.ClusterServiceBrokers().Informer().GetStore().Add(getTestClusterServiceBroker())
	sharedInformers.ServiceClasses().Informer().GetStore().Add(getTestNonbindableServiceClass())
	sharedInformers.ServiceInstances().Informer().GetStore().Add(func() *v1alpha1.ServiceInstance {
		i := getTestServiceInstanceNonbindableServiceBindablePlan()
		i.Status = v1alpha1.ServiceInstanceStatus{
			Conditions: []v1alpha1.ServiceInstanceCondition{
				{
					Type:   v1alpha1.ServiceInstanceConditionReady,
					Status: v1alpha1.ConditionTrue,
				},
			},
		}
		return i
	}())
	sharedInformers.ServicePlans().Informer().GetStore().Add(getTestServicePlan())

	binding := &v1alpha1.ServiceInstanceCredential{
		ObjectMeta: metav1.ObjectMeta{
			Name:       testServiceInstanceCredentialName,
			Namespace:  testNamespace,
			Generation: 1,
		},
		Spec: v1alpha1.ServiceInstanceCredentialSpec{
			ServiceInstanceRef: v1.LocalObjectReference{Name: testServiceInstanceName},
			ExternalID:         bindingGUID,
			SecretName:         testServiceInstanceCredentialSecretName,
		},
	}

	err := testController.reconcileServiceInstanceCredential(binding)
	if err != nil {
		t.Fatalf("A bindable plan overrides the bindability of a service class: %v", err)
	}

	brokerActions := fakeClusterServiceBrokerClient.Actions()
	assertNumberOfClusterServiceBrokerActions(t, brokerActions, 1)
	assertBind(t, brokerActions[0], &osb.BindRequest{
		BindingID:  bindingGUID,
		InstanceID: instanceGUID,
		ServiceID:  nonbindableServiceClassGUID,
		PlanID:     planGUID,
		AppGUID:    strPtr(testNsUID),
		BindResource: &osb.BindResource{
			AppGUID: strPtr(testNsUID),
		},
	})

	actions := fakeCatalogClient.Actions()
	assertNumberOfActions(t, actions, 2)

	updatedServiceInstanceCredential := assertUpdateStatus(t, actions[0], binding)
	assertServiceInstanceCredentialOperationInProgress(t, updatedServiceInstanceCredential, v1alpha1.ServiceInstanceCredentialOperationBind, binding)
	assertServiceInstanceCredentialOrphanMitigationSet(t, updatedServiceInstanceCredential, false)

	updatedServiceInstanceCredential = assertUpdateStatus(t, actions[1], binding)
	assertServiceInstanceCredentialOperationSuccess(t, updatedServiceInstanceCredential, v1alpha1.ServiceInstanceCredentialOperationBind, binding)
	assertServiceInstanceCredentialOrphanMitigationSet(t, updatedServiceInstanceCredential, false)

	kubeActions := fakeKubeClient.Actions()
	assertNumberOfActions(t, kubeActions, 3)

	// first action is a get on the namespace
	// second action is a get on the secret
	action := kubeActions[2].(clientgotesting.CreateAction)
	if e, a := "create", action.GetVerb(); e != a {
		t.Fatalf("Unexpected verb on action; expected %v, got %v", e, a)
	}
	if e, a := "secrets", action.GetResource().Resource; e != a {
		t.Fatalf("Unexpected resource on action; expected %v, got %v", e, a)
	}
	actionSecret, ok := action.GetObject().(*v1.Secret)
	if !ok {
		t.Fatal("couldn't convert secret into a v1.Secret")
	}
	if e, a := testServiceInstanceCredentialSecretName, actionSecret.Name; e != a {
		t.Fatalf("Unexpected name of secret; expected %v, got %v", e, a)
	}
	value, ok := actionSecret.Data["a"]
	if !ok {
		t.Fatal("Didn't find secret key 'a' in created secret")
	}
	if e, a := "b", string(value); e != a {
		t.Fatalf("Unexpected value of key 'a' in created secret; expected %v got %v", e, a)
	}
	value, ok = actionSecret.Data["c"]
	if !ok {
		t.Fatal("Didn't find secret key 'a' in created secret")
	}
	if e, a := "d", string(value); e != a {
		t.Fatalf("Unexpected value of key 'c' in created secret; expected %v got %v", e, a)
	}

	events := getRecordedEvents(testController)
	assertNumEvents(t, events, 1)
}

// TestReconcileBindingBindableServiceClassNonbindablePlan tests reconcileBinding
// to ensure a binding for an instance that references a bindable service class
// and a non-bindable plan fails as expected.
func TestReconcileServiceInstanceCredentialBindableServiceClassNonbindablePlan(t *testing.T) {
	_, fakeCatalogClient, fakeClusterServiceBrokerClient, testController, sharedInformers := newTestController(t, noFakeActions())

	sharedInformers.ClusterServiceBrokers().Informer().GetStore().Add(getTestClusterServiceBroker())
	sharedInformers.ServiceClasses().Informer().GetStore().Add(getTestServiceClass())
	sharedInformers.ServiceInstances().Informer().GetStore().Add(getTestServiceInstanceBindableServiceNonbindablePlan())
	sharedInformers.ServicePlans().Informer().GetStore().Add(getTestServicePlanNonbindable())

	binding := &v1alpha1.ServiceInstanceCredential{
		ObjectMeta: metav1.ObjectMeta{
			Name:       testServiceInstanceCredentialName,
			Namespace:  testNamespace,
			Generation: 1,
		},
		Spec: v1alpha1.ServiceInstanceCredentialSpec{
			ServiceInstanceRef: v1.LocalObjectReference{Name: testServiceInstanceName},
			ExternalID:         bindingGUID,
		},
	}

	err := testController.reconcileServiceInstanceCredential(binding)
	if err != nil {
		t.Fatalf("binding against a nonbindable plan should fail")
	}

	brokerActions := fakeClusterServiceBrokerClient.Actions()
	assertNumberOfClusterServiceBrokerActions(t, brokerActions, 0)

	actions := fakeCatalogClient.Actions()
	assertNumberOfActions(t, actions, 1)

	// There should only be one action that says binding was created
	updatedServiceInstanceCredential := assertUpdateStatus(t, actions[0], binding)
	assertServiceInstanceCredentialErrorBeforeRequest(t, updatedServiceInstanceCredential, errorNonbindableServiceClassReason, binding)
	assertServiceInstanceCredentialOrphanMitigationSet(t, updatedServiceInstanceCredential, false)

	events := getRecordedEvents(testController)
	assertNumEvents(t, events, 1)

	expectedEvent := apiv1.EventTypeWarning + " " + errorNonbindableServiceClassReason + ` ServiceInstanceCredential "test-ns/test-binding" references a non-bindable ServiceClass ("test-serviceclass") and Plan ("test-unbindable-plan") combination`
	if e, a := expectedEvent, events[0]; e != a {
		t.Fatalf("Received unexpected event: %v", a)
	}
}

// TestReconcileBindingFailsWithInstanceAsyncOngoing tests reconcileBinding
// to ensure a binding that references an instance that has the
// AsyncOpInProgreset flag set to true fails as expected.
func TestReconcileServiceInstanceCredentialFailsWithServiceInstanceAsyncOngoing(t *testing.T) {
	fakeKubeClient, fakeCatalogClient, fakeClusterServiceBrokerClient, testController, sharedInformers := newTestController(t, noFakeActions())

	sharedInformers.ClusterServiceBrokers().Informer().GetStore().Add(getTestClusterServiceBroker())
	sharedInformers.ServiceClasses().Informer().GetStore().Add(getTestServiceClass())
	sharedInformers.ServiceInstances().Informer().GetStore().Add(getTestServiceInstanceAsyncProvisioning(""))
	sharedInformers.ServicePlans().Informer().GetStore().Add(getTestServicePlan())

	binding := &v1alpha1.ServiceInstanceCredential{
		ObjectMeta: metav1.ObjectMeta{
			Name:       testServiceInstanceCredentialName,
			Namespace:  testNamespace,
			Generation: 1,
		},
		Spec: v1alpha1.ServiceInstanceCredentialSpec{
			ServiceInstanceRef: v1.LocalObjectReference{Name: testServiceInstanceName},
			ExternalID:         bindingGUID,
		},
	}

	err := testController.reconcileServiceInstanceCredential(binding)
	if err == nil {
		t.Fatalf("reconcileServiceInstanceCredential did not fail with async operation ongoing")
	}

	if !strings.Contains(err.Error(), "Ongoing Asynchronous") {
		t.Fatalf("Did not get the expected error %q : got %q", "Ongoing Asynchronous", err)
	}

	brokerActions := fakeClusterServiceBrokerClient.Actions()
	assertNumberOfClusterServiceBrokerActions(t, brokerActions, 0)

	// verify no kube resources created.
	// No actions
	kubeActions := fakeKubeClient.Actions()
	assertNumberOfActions(t, kubeActions, 0)

	actions := fakeCatalogClient.Actions()
	assertNumberOfActions(t, actions, 1)

	// There should only be one action that says binding was created
	updatedServiceInstanceCredential := assertUpdateStatus(t, actions[0], binding)
	assertServiceInstanceCredentialErrorBeforeRequest(t, updatedServiceInstanceCredential, errorWithOngoingAsyncOperation, binding)
	assertServiceInstanceCredentialOrphanMitigationSet(t, updatedServiceInstanceCredential, false)

	events := getRecordedEvents(testController)
	assertNumEvents(t, events, 1)

	if !strings.Contains(events[0], "has ongoing asynchronous operation") {
		t.Fatalf("Did not find expected error %q : got %q", "has ongoing asynchronous operation", events[0])
	}
	if !strings.Contains(events[0], testNamespace+"/"+testServiceInstanceName) {
		t.Fatalf("Did not find expected instance name : got %q", events[0])
	}
	if !strings.Contains(events[0], testNamespace+"/"+testServiceInstanceCredentialName) {
		t.Fatalf("Did not find expected binding name : got %q", events[0])
	}
}

// TestReconcileBindingInstanceNotReady tests reconcileBinding to ensure a
// binding for an instance with a ready condition set to false fails as expected.
func TestReconcileServiceInstanceCredentialServiceInstanceNotReady(t *testing.T) {
	fakeKubeClient, fakeCatalogClient, fakeClusterServiceBrokerClient, testController, sharedInformers := newTestController(t, noFakeActions())

	addGetNamespaceReaction(fakeKubeClient)

	sharedInformers.ClusterServiceBrokers().Informer().GetStore().Add(getTestClusterServiceBroker())
	sharedInformers.ServiceClasses().Informer().GetStore().Add(getTestServiceClass())
	sharedInformers.ServiceInstances().Informer().GetStore().Add(getTestServiceInstanceWithRefs())
	sharedInformers.ServicePlans().Informer().GetStore().Add(getTestServicePlan())

	binding := &v1alpha1.ServiceInstanceCredential{
		ObjectMeta: metav1.ObjectMeta{
			Name:       testServiceInstanceCredentialName,
			Namespace:  testNamespace,
			Generation: 1,
		},
		Spec: v1alpha1.ServiceInstanceCredentialSpec{
			ServiceInstanceRef: v1.LocalObjectReference{Name: testServiceInstanceName},
			ExternalID:         bindingGUID,
		},
	}

	err := testController.reconcileServiceInstanceCredential(binding)
	if err != nil {
		t.Fatalf("a binding cannot be created against an instance that is not prepared")
	}

	brokerActions := fakeClusterServiceBrokerClient.Actions()
	assertNumberOfClusterServiceBrokerActions(t, brokerActions, 0)

	actions := fakeCatalogClient.Actions()
	assertNumberOfActions(t, actions, 1)

	// There should only be one action that says binding was created
	updatedServiceInstanceCredential := assertUpdateStatus(t, actions[0], binding)
	assertServiceInstanceCredentialErrorBeforeRequest(t, updatedServiceInstanceCredential, errorServiceInstanceNotReadyReason, binding)
	assertServiceInstanceCredentialOrphanMitigationSet(t, updatedServiceInstanceCredential, false)

	events := getRecordedEvents(testController)
	assertNumEvents(t, events, 1)

	expectedEvent := apiv1.EventTypeWarning + " " + errorServiceInstanceNotReadyReason + " " + `ServiceInstanceCredential cannot begin because referenced instance "test-ns/test-instance" is not ready`
	if e, a := expectedEvent, events[0]; e != a {
		t.Fatalf("Received unexpected event: %v", a)
	}
}

// TestReconcileBindingNamespaceError tests reconcileBinding to ensure a binding
// with an invalid namespace fails as expected.
func TestReconcileServiceInstanceCredentialNamespaceError(t *testing.T) {
	fakeKubeClient, fakeCatalogClient, fakeClusterServiceBrokerClient, testController, sharedInformers := newTestController(t, noFakeActions())

	fakeKubeClient.AddReactor("get", "namespaces", func(action clientgotesting.Action) (bool, runtime.Object, error) {
		return true, &v1.Namespace{}, errors.New("No namespace")
	})

	sharedInformers.ClusterServiceBrokers().Informer().GetStore().Add(getTestClusterServiceBroker())
	sharedInformers.ServiceClasses().Informer().GetStore().Add(getTestServiceClass())
	sharedInformers.ServiceInstances().Informer().GetStore().Add(getTestServiceInstanceWithRefs())
	sharedInformers.ServicePlans().Informer().GetStore().Add(getTestServicePlan())

	binding := &v1alpha1.ServiceInstanceCredential{
		ObjectMeta: metav1.ObjectMeta{
			Name:       testServiceInstanceCredentialName,
			Namespace:  testNamespace,
			Generation: 1,
		},
		Spec: v1alpha1.ServiceInstanceCredentialSpec{
			ServiceInstanceRef: v1.LocalObjectReference{Name: testServiceInstanceName},
			ExternalID:         bindingGUID,
		},
	}

	err := testController.reconcileServiceInstanceCredential(binding)
	if err == nil {
		t.Fatalf("ServiceInstanceCredentials are namespaced. If we cannot get the namespace we cannot find the binding")
	}

	brokerActions := fakeClusterServiceBrokerClient.Actions()
	assertNumberOfClusterServiceBrokerActions(t, brokerActions, 0)

	actions := fakeCatalogClient.Actions()
	assertNumberOfActions(t, actions, 1)

	updatedServiceInstanceCredential := assertUpdateStatus(t, actions[0], binding)
	assertServiceInstanceCredentialErrorBeforeRequest(t, updatedServiceInstanceCredential, errorFindingNamespaceServiceInstanceReason, binding)
	assertServiceInstanceCredentialOrphanMitigationSet(t, updatedServiceInstanceCredential, false)

	events := getRecordedEvents(testController)
	assertNumEvents(t, events, 1)

	expectedEvent := apiv1.EventTypeWarning + " " + errorFindingNamespaceServiceInstanceReason + " " + "Failed to get namespace \"test-ns\" during binding: No namespace"
	if e, a := expectedEvent, events[0]; e != a {
		t.Fatalf("Received unexpected event: %v", a)
	}
}

// TestReconcileBindingDelete tests reconcileBinding to ensure a binding
// deletion works as expected.
func TestReconcileServiceInstanceCredentialDelete(t *testing.T) {
	fakeKubeClient, fakeCatalogClient, fakeClusterServiceBrokerClient, testController, sharedInformers := newTestController(t, fakeosb.FakeClientConfiguration{
		UnbindReaction: &fakeosb.UnbindReaction{},
	})

	sharedInformers.ClusterServiceBrokers().Informer().GetStore().Add(getTestClusterServiceBroker())
	sharedInformers.ServiceClasses().Informer().GetStore().Add(getTestServiceClass())
	sharedInformers.ServiceInstances().Informer().GetStore().Add(getTestServiceInstanceWithRefs())
	sharedInformers.ServicePlans().Informer().GetStore().Add(getTestServicePlan())

	binding := &v1alpha1.ServiceInstanceCredential{
		ObjectMeta: metav1.ObjectMeta{
			Name:              testServiceInstanceCredentialName,
			Namespace:         testNamespace,
			DeletionTimestamp: &metav1.Time{},
			Finalizers:        []string{v1alpha1.FinalizerServiceCatalog},
			Generation:        2,
		},
		Spec: v1alpha1.ServiceInstanceCredentialSpec{
			ServiceInstanceRef: v1.LocalObjectReference{Name: testServiceInstanceName},
			ExternalID:         bindingGUID,
			SecretName:         testServiceInstanceCredentialSecretName,
		},
		Status: v1alpha1.ServiceInstanceCredentialStatus{
			ReconciledGeneration: 1,
			ExternalProperties:   &v1alpha1.ServiceInstanceCredentialPropertiesState{},
		},
	}

	fakeCatalogClient.AddReactor("get", "serviceinstancecredentials", func(action clientgotesting.Action) (bool, runtime.Object, error) {
		return true, binding, nil
	})

	err := testController.reconcileServiceInstanceCredential(binding)
	if err != nil {
		t.Fatalf("%v", err)
	}

	brokerActions := fakeClusterServiceBrokerClient.Actions()
	assertNumberOfClusterServiceBrokerActions(t, brokerActions, 1)
	assertUnbind(t, brokerActions[0], &osb.UnbindRequest{
		BindingID:  bindingGUID,
		InstanceID: instanceGUID,
		ServiceID:  serviceClassGUID,
		PlanID:     planGUID,
	})

	kubeActions := fakeKubeClient.Actions()
	// The action should be deleting the secret
	assertNumberOfActions(t, kubeActions, 1)

	deleteAction := kubeActions[0].(clientgotesting.DeleteActionImpl)
	if e, a := "delete", deleteAction.GetVerb(); e != a {
		t.Fatalf("Unexpected verb on kubeActions[1]; expected %v, got %v", e, a)
	}

	if e, a := binding.Spec.SecretName, deleteAction.Name; e != a {
		t.Fatalf("Unexpected name of secret: expected %v, got %v", e, a)
	}

	actions := fakeCatalogClient.Actions()
	// The actions should be:
	// 0. Updating the current operation
	// 1. Updating the ready condition
	assertNumberOfActions(t, actions, 2)

	updatedServiceInstanceCredential := assertUpdateStatus(t, actions[0], binding)
	assertServiceInstanceCredentialOperationInProgress(t, updatedServiceInstanceCredential, v1alpha1.ServiceInstanceCredentialOperationUnbind, binding)
	assertServiceInstanceCredentialOrphanMitigationSet(t, updatedServiceInstanceCredential, false)

	updatedServiceInstanceCredential = assertUpdateStatus(t, actions[1], binding)
	assertServiceInstanceCredentialOperationSuccess(t, updatedServiceInstanceCredential, v1alpha1.ServiceInstanceCredentialOperationUnbind, binding)
	assertServiceInstanceCredentialOrphanMitigationSet(t, updatedServiceInstanceCredential, false)

	events := getRecordedEvents(testController)
	assertNumEvents(t, events, 1)

	expectedEvent := apiv1.EventTypeNormal + " " + successUnboundReason + " " + "This binding was deleted successfully"
	if e, a := expectedEvent, events[0]; e != a {
		t.Fatalf("Received unexpected event: %v", a)
	}
}

// TestSetServiceInstanceCredentialCondition verifies setting a condition on a binding yields
// the results as expected with respect to the changed condition and transition
// time.
func TestSetServiceInstanceCredentialCondition(t *testing.T) {
	bindingWithCondition := func(condition *v1alpha1.ServiceInstanceCredentialCondition) *v1alpha1.ServiceInstanceCredential {
		binding := getTestServiceInstanceCredential()
		binding.Status = v1alpha1.ServiceInstanceCredentialStatus{
			Conditions: []v1alpha1.ServiceInstanceCredentialCondition{*condition},
		}

		return binding
	}

	// The value of the LastTransitionTime field on conditions has to be
	// tested to ensure it is updated correctly.
	//
	// Time basis for all condition changes:
	newTs := metav1.Now()
	oldTs := metav1.NewTime(newTs.Add(-5 * time.Minute))

	// condition is a shortcut method for creating conditions with the 'old' timestamp.
	condition := func(cType v1alpha1.ServiceInstanceCredentialConditionType, status v1alpha1.ConditionStatus, s ...string) *v1alpha1.ServiceInstanceCredentialCondition {
		c := &v1alpha1.ServiceInstanceCredentialCondition{
			Type:   cType,
			Status: status,
		}

		if len(s) > 0 {
			c.Reason = s[0]
		}

		if len(s) > 1 {
			c.Message = s[1]
		}

		// This is the expected 'before' timestamp for all conditions under
		// test.
		c.LastTransitionTime = oldTs

		return c
	}

	// shortcut methods for creating conditions of different types

	readyFalse := func() *v1alpha1.ServiceInstanceCredentialCondition {
		return condition(v1alpha1.ServiceInstanceCredentialConditionReady, v1alpha1.ConditionFalse, "Reason", "Message")
	}

	readyFalsef := func(reason, message string) *v1alpha1.ServiceInstanceCredentialCondition {
		return condition(v1alpha1.ServiceInstanceCredentialConditionReady, v1alpha1.ConditionFalse, reason, message)
	}

	readyTrue := func() *v1alpha1.ServiceInstanceCredentialCondition {
		return condition(v1alpha1.ServiceInstanceCredentialConditionReady, v1alpha1.ConditionTrue, "Reason", "Message")
	}

	failedTrue := func() *v1alpha1.ServiceInstanceCredentialCondition {
		return condition(v1alpha1.ServiceInstanceCredentialConditionFailed, v1alpha1.ConditionTrue, "Reason", "Message")
	}

	// withNewTs sets the LastTransitionTime to the 'new' basis time and
	// returns it.
	withNewTs := func(c *v1alpha1.ServiceInstanceCredentialCondition) *v1alpha1.ServiceInstanceCredentialCondition {
		c.LastTransitionTime = newTs
		return c
	}

	// this test works by calling setServiceInstanceCredentialCondition with the input and
	// condition fields of the test case, and ensuring that afterward the
	// input (which is mutated by the setServiceInstanceCredentialCondition call) is deep-equal
	// to the test case result.
	//
	// take note of where withNewTs is used when declaring the result to
	// indicate that the LastTransitionTime field on a condition should have
	// changed.
	cases := []struct {
		name      string
		input     *v1alpha1.ServiceInstanceCredential
		condition *v1alpha1.ServiceInstanceCredentialCondition
		result    *v1alpha1.ServiceInstanceCredential
	}{
		{
			name:      "new ready condition",
			input:     getTestServiceInstanceCredential(),
			condition: readyFalse(),
			result:    bindingWithCondition(withNewTs(readyFalse())),
		},
		{
			name:      "not ready -> not ready; no ts update",
			input:     bindingWithCondition(readyFalse()),
			condition: readyFalse(),
			result:    bindingWithCondition(readyFalse()),
		},
		{
			name:      "not ready -> not ready, reason and message change; no ts update",
			input:     bindingWithCondition(readyFalse()),
			condition: readyFalsef("DifferentReason", "DifferentMessage"),
			result:    bindingWithCondition(readyFalsef("DifferentReason", "DifferentMessage")),
		},
		{
			name:      "not ready -> ready",
			input:     bindingWithCondition(readyFalse()),
			condition: readyTrue(),
			result:    bindingWithCondition(withNewTs(readyTrue())),
		},
		{
			name:      "ready -> ready; no ts update",
			input:     bindingWithCondition(readyTrue()),
			condition: readyTrue(),
			result:    bindingWithCondition(readyTrue()),
		},
		{
			name:      "ready -> not ready",
			input:     bindingWithCondition(readyTrue()),
			condition: readyFalse(),
			result:    bindingWithCondition(withNewTs(readyFalse())),
		},
		{
			name:      "not ready -> not ready + failed",
			input:     bindingWithCondition(readyFalse()),
			condition: failedTrue(),
			result: func() *v1alpha1.ServiceInstanceCredential {
				i := bindingWithCondition(readyFalse())
				i.Status.Conditions = append(i.Status.Conditions, *withNewTs(failedTrue()))
				return i
			}(),
		},
	}

	for _, tc := range cases {
		setServiceInstanceCredentialConditionInternal(tc.input, tc.condition.Type, tc.condition.Status, tc.condition.Reason, tc.condition.Message, newTs)

		if !reflect.DeepEqual(tc.input, tc.result) {
			t.Errorf("%v: unexpected diff: %v", tc.name, diff.ObjectReflectDiff(tc.input, tc.result))
		}
	}
}

// TestReconcileServiceInstanceCredentialDeleteFailedServiceInstanceCredential tests reconcileServiceInstanceCredential to ensure
// a binding with a failed status is deleted properly.
func TestReconcileServiceInstanceCredentialDeleteFailedServiceInstanceCredential(t *testing.T) {
	fakeKubeClient, fakeCatalogClient, fakeClusterServiceBrokerClient, testController, sharedInformers := newTestController(t, fakeosb.FakeClientConfiguration{
		UnbindReaction: &fakeosb.UnbindReaction{},
	})

	sharedInformers.ClusterServiceBrokers().Informer().GetStore().Add(getTestClusterServiceBroker())
	sharedInformers.ServiceClasses().Informer().GetStore().Add(getTestServiceClass())
	sharedInformers.ServiceInstances().Informer().GetStore().Add(getTestServiceInstanceWithRefs())
	sharedInformers.ServicePlans().Informer().GetStore().Add(getTestServicePlan())

	binding := getTestServiceInstanceCredentialWithFailedStatus()
	binding.ObjectMeta.DeletionTimestamp = &metav1.Time{}
	binding.ObjectMeta.Finalizers = []string{v1alpha1.FinalizerServiceCatalog}
	binding.Status.ExternalProperties = &v1alpha1.ServiceInstanceCredentialPropertiesState{}

	binding.ObjectMeta.Generation = 2
	binding.Status.ReconciledGeneration = 1

	fakeCatalogClient.AddReactor("get", "serviceinstancecredentials", func(action clientgotesting.Action) (bool, runtime.Object, error) {
		return true, binding, nil
	})

	err := testController.reconcileServiceInstanceCredential(binding)
	if err != nil {
		t.Fatalf("%v", err)
	}

	brokerActions := fakeClusterServiceBrokerClient.Actions()
	assertNumberOfClusterServiceBrokerActions(t, brokerActions, 1)
	assertUnbind(t, brokerActions[0], &osb.UnbindRequest{
		BindingID:  bindingGUID,
		InstanceID: instanceGUID,
		ServiceID:  serviceClassGUID,
		PlanID:     planGUID,
	})

	// verify one kube action occurred
	kubeActions := fakeKubeClient.Actions()
	if err := checkKubeClientActions(kubeActions, []kubeClientAction{
		{verb: "delete", resourceName: "secrets", checkType: checkGetActionType},
	}); err != nil {
		t.Fatal(err)
	}

	deleteAction := kubeActions[0].(clientgotesting.DeleteActionImpl)
	if e, a := binding.Spec.SecretName, deleteAction.Name; e != a {
		t.Fatalf("Unexpected name of secret: expected %v, got %v", e, a)
	}

	actions := fakeCatalogClient.Actions()
	// The four actions should be:
	// 0. Updating the current operation
	// 1. Updating the ready condition
	assertNumberOfActions(t, actions, 2)

	updatedServiceInstanceCredential := assertUpdateStatus(t, actions[0], binding)
	assertServiceInstanceCredentialOperationInProgress(t, updatedServiceInstanceCredential, v1alpha1.ServiceInstanceCredentialOperationUnbind, binding)
	assertServiceInstanceCredentialOrphanMitigationSet(t, updatedServiceInstanceCredential, false)

	updatedServiceInstanceCredential = assertUpdateStatus(t, actions[1], binding)
	assertServiceInstanceCredentialOperationSuccess(t, updatedServiceInstanceCredential, v1alpha1.ServiceInstanceCredentialOperationUnbind, binding)
	assertServiceInstanceCredentialOrphanMitigationSet(t, updatedServiceInstanceCredential, false)

	events := getRecordedEvents(testController)
	assertNumEvents(t, events, 1)

	expectedEvent := apiv1.EventTypeNormal + " " + successUnboundReason + " " + "This binding was deleted successfully"
	if e, a := expectedEvent, events[0]; e != a {
		t.Fatalf("Received unexpected event: %v", a)
	}
}

// TestReconcileBindingWithBrokerError tests reconcileBinding to ensure a
// binding request response that contains a broker error fails as expected.
func TestReconcileServiceInstanceCredentialWithClusterServiceBrokerError(t *testing.T) {
	_, fakeCatalogClient, _, testController, sharedInformers := newTestController(t, fakeosb.FakeClientConfiguration{
		BindReaction: &fakeosb.BindReaction{
			Response: &osb.BindResponse{
				Credentials: map[string]interface{}{
					"a": "b",
					"c": "d",
				},
			},
			Error: fakeosb.UnexpectedActionError(),
		},
	})

	sharedInformers.ClusterServiceBrokers().Informer().GetStore().Add(getTestClusterServiceBroker())
	sharedInformers.ServiceClasses().Informer().GetStore().Add(getTestServiceClass())
	sharedInformers.ServiceInstances().Informer().GetStore().Add(getTestServiceInstanceWithStatus(v1alpha1.ConditionTrue))
	sharedInformers.ServicePlans().Informer().GetStore().Add(getTestServicePlan())

	binding := &v1alpha1.ServiceInstanceCredential{
		ObjectMeta: metav1.ObjectMeta{
			Name:       testServiceInstanceCredentialName,
			Namespace:  testNamespace,
			Generation: 1,
		},
		Spec: v1alpha1.ServiceInstanceCredentialSpec{
			ServiceInstanceRef: v1.LocalObjectReference{Name: testServiceInstanceName},
			ExternalID:         bindingGUID,
			SecretName:         testServiceInstanceCredentialSecretName,
		},
	}

	err := testController.reconcileServiceInstanceCredential(binding)
	if err == nil {
		t.Fatal("reconcileServiceInstanceCredential should have returned an error")
	}

	actions := fakeCatalogClient.Actions()
	assertNumberOfActions(t, actions, 2)

	updatedServiceInstanceCredential := assertUpdateStatus(t, actions[0], binding)
	assertServiceInstanceCredentialOperationInProgress(t, updatedServiceInstanceCredential, v1alpha1.ServiceInstanceCredentialOperationBind, binding)
	assertServiceInstanceCredentialOrphanMitigationSet(t, updatedServiceInstanceCredential, false)

	updatedServiceInstanceCredential = assertUpdateStatus(t, actions[1], binding)
	assertServiceInstanceCredentialRequestRetriableError(t, updatedServiceInstanceCredential, v1alpha1.ServiceInstanceCredentialOperationBind, errorBindCallReason, binding)
	assertServiceInstanceCredentialOrphanMitigationSet(t, updatedServiceInstanceCredential, false)

	events := getRecordedEvents(testController)
	expectedEvent := apiv1.EventTypeWarning + " " + errorBindCallReason + " " + `Error creating ServiceInstanceCredential "test-binding/test-ns" for ServiceInstance "test-ns/test-instance" of ServiceClass "test-serviceclass" at ClusterServiceBroker "test-broker": Unexpected action`
	if 1 != len(events) {
		t.Fatalf("Did not record expected event, expecting: %v", expectedEvent)
	}
	if e, a := expectedEvent, events[0]; e != a {
		t.Fatalf("Received unexpected event: %v, expecting: %v", a, e)
	}
}

// TestReconcileBindingWithBrokerHTTPError tests reconcileBindings to ensure a
// binding request response that contains a broker HTTP error fails as expected.
func TestReconcileServiceInstanceCredentialWithClusterServiceBrokerHTTPError(t *testing.T) {
	_, fakeCatalogClient, _, testController, sharedInformers := newTestController(t, fakeosb.FakeClientConfiguration{
		BindReaction: &fakeosb.BindReaction{
			Response: &osb.BindResponse{
				Credentials: map[string]interface{}{
					"a": "b",
					"c": "d",
				},
			},
			Error: fakeosb.AsyncRequiredError(),
		},
	})

	sharedInformers.ClusterServiceBrokers().Informer().GetStore().Add(getTestClusterServiceBroker())
	sharedInformers.ServiceClasses().Informer().GetStore().Add(getTestServiceClass())
	sharedInformers.ServiceInstances().Informer().GetStore().Add(getTestServiceInstanceWithStatus(v1alpha1.ConditionTrue))
	sharedInformers.ServicePlans().Informer().GetStore().Add(getTestServicePlan())

	binding := &v1alpha1.ServiceInstanceCredential{
		ObjectMeta: metav1.ObjectMeta{
			Name:       testServiceInstanceCredentialName,
			Namespace:  testNamespace,
			Generation: 1,
		},
		Spec: v1alpha1.ServiceInstanceCredentialSpec{
			ServiceInstanceRef: v1.LocalObjectReference{Name: testServiceInstanceName},
			ExternalID:         bindingGUID,
			SecretName:         testServiceInstanceCredentialSecretName,
		},
	}

	err := testController.reconcileServiceInstanceCredential(binding)
	if err != nil {
		t.Fatal("reconcileServiceInstanceCredential should not have returned an error")
	}

	actions := fakeCatalogClient.Actions()
	assertNumberOfActions(t, actions, 2)

	updatedServiceInstanceCredential := assertUpdateStatus(t, actions[0], binding)
	assertServiceInstanceCredentialOperationInProgress(t, updatedServiceInstanceCredential, v1alpha1.ServiceInstanceCredentialOperationBind, binding)
	assertServiceInstanceCredentialOrphanMitigationSet(t, updatedServiceInstanceCredential, false)

	updatedServiceInstanceCredential = assertUpdateStatus(t, actions[1], binding)
	assertServiceInstanceCredentialRequestFailingError(t, updatedServiceInstanceCredential, v1alpha1.ServiceInstanceCredentialOperationBind, errorBindCallReason, "ServiceInstanceCredentialReturnedFailure", binding)
	assertServiceInstanceCredentialOrphanMitigationSet(t, updatedServiceInstanceCredential, false)

	events := getRecordedEvents(testController)
	expectedEvent := apiv1.EventTypeWarning + " " + errorBindCallReason + " " + `Error creating ServiceInstanceCredential "test-binding/test-ns" for ServiceInstance "test-ns/test-instance" of ServiceClass "test-serviceclass" at ClusterServiceBroker "test-broker", Status: 422; ErrorMessage: AsyncRequired; Description: This service plan requires client support for asynchronous service operations.; ResponseError: <nil>`
	if 1 != len(events) {
		t.Fatalf("Did not record expected event, expecting: %v", expectedEvent)
	}
	if e, a := expectedEvent, events[0]; e != a {
		t.Fatalf("Received unexpected event: '%v', expecting: '%v'", a, e)
	}
}

// TestReconcileServiceInstanceCredentialWithFailureCondition tests reconcileServiceInstanceCredential to ensure
// no processing is done on a binding containing a failed status.
func TestReconcileServiceInstanceCredentialWithFailureCondition(t *testing.T) {
	fakeKubeClient, fakeCatalogClient, fakeClusterServiceBrokerClient, testController, sharedInformers := newTestController(t, noFakeActions())

	sharedInformers.ClusterServiceBrokers().Informer().GetStore().Add(getTestClusterServiceBroker())
	sharedInformers.ServiceClasses().Informer().GetStore().Add(getTestServiceClass())
	sharedInformers.ServiceInstances().Informer().GetStore().Add(getTestServiceInstanceWithStatus(v1alpha1.ConditionTrue))
	sharedInformers.ServicePlans().Informer().GetStore().Add(getTestServicePlan())

	binding := getTestServiceInstanceCredentialWithFailedStatus()

	if err := testController.reconcileServiceInstanceCredential(binding); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	kubeActions := fakeKubeClient.Actions()
	assertNumberOfActions(t, kubeActions, 0)

	actions := fakeCatalogClient.Actions()
	assertNumberOfActions(t, actions, 0)

	brokerActions := fakeClusterServiceBrokerClient.Actions()
	assertNumberOfClusterServiceBrokerActions(t, brokerActions, 0)

	events := getRecordedEvents(testController)
	assertNumEvents(t, events, 0)
}

// TestReconcileServiceInstanceCredentialWithServiceInstanceCredentialCallFailure tests reconcileServiceInstanceCredential to ensure
// a bind creation failure is handled properly.
func TestReconcileServiceInstanceCredentialWithServiceInstanceCredentialCallFailure(t *testing.T) {
	fakeKubeClient, fakeCatalogClient, fakeClusterServiceBrokerClient, testController, sharedInformers := newTestController(t, fakeosb.FakeClientConfiguration{
		BindReaction: &fakeosb.BindReaction{
			Error: errors.New("fake creation failure"),
		},
	})

	sharedInformers.ClusterServiceBrokers().Informer().GetStore().Add(getTestClusterServiceBroker())
	sharedInformers.ServiceClasses().Informer().GetStore().Add(getTestServiceClass())
	sharedInformers.ServiceInstances().Informer().GetStore().Add(getTestServiceInstanceWithStatus(v1alpha1.ConditionTrue))
	sharedInformers.ServicePlans().Informer().GetStore().Add(getTestServicePlan())

	binding := getTestServiceInstanceCredential()

	if err := testController.reconcileServiceInstanceCredential(binding); err == nil {
		t.Fatal("ServiceInstanceCredential creation should fail")
	}

	// verify one kube action occurred
	kubeActions := fakeKubeClient.Actions()
	if err := checkKubeClientActions(kubeActions, []kubeClientAction{
		{verb: "get", resourceName: "namespaces", checkType: checkGetActionType},
	}); err != nil {
		t.Fatal(err)
	}

	actions := fakeCatalogClient.Actions()
	assertNumberOfActions(t, actions, 2)

	updatedServiceInstanceCredential := assertUpdateStatus(t, actions[0], binding)
	assertServiceInstanceCredentialOperationInProgress(t, updatedServiceInstanceCredential, v1alpha1.ServiceInstanceCredentialOperationBind, binding)
	assertServiceInstanceCredentialOrphanMitigationSet(t, updatedServiceInstanceCredential, false)

	updatedServiceInstanceCredential = assertUpdateStatus(t, actions[1], binding)
	assertServiceInstanceCredentialRequestRetriableError(t, updatedServiceInstanceCredential, v1alpha1.ServiceInstanceCredentialOperationBind, errorBindCallReason, binding)
	assertServiceInstanceCredentialOrphanMitigationSet(t, updatedServiceInstanceCredential, false)

	brokerActions := fakeClusterServiceBrokerClient.Actions()
	assertNumberOfClusterServiceBrokerActions(t, brokerActions, 1)
	assertBind(t, brokerActions[0], &osb.BindRequest{
		BindingID:  bindingGUID,
		InstanceID: instanceGUID,
		ServiceID:  serviceClassGUID,
		PlanID:     planGUID,
		AppGUID:    strPtr(""),
		BindResource: &osb.BindResource{
			AppGUID: strPtr(""),
		},
	})

	events := getRecordedEvents(testController)
	assertNumEvents(t, events, 1)

	expectedEvent := apiv1.EventTypeWarning + " " + errorBindCallReason + " " + "Error creating ServiceInstanceCredential \"test-binding/test-ns\" for ServiceInstance \"test-ns/test-instance\" of ServiceClass \"test-serviceclass\" at ClusterServiceBroker \"test-broker\": fake creation failure"

	if e, a := expectedEvent, events[0]; e != a {
		t.Fatalf("Received unexpected event: %v", a)
	}
}

// TestReconcileServiceInstanceCredentialWithServiceInstanceCredentialFailure tests reconcileServiceInstanceCredential to ensure
// a binding request that receives an error from the broker is handled properly.
func TestReconcileServiceInstanceCredentialWithServiceInstanceCredentialFailure(t *testing.T) {
	fakeKubeClient, fakeCatalogClient, fakeClusterServiceBrokerClient, testController, sharedInformers := newTestController(t, fakeosb.FakeClientConfiguration{
		BindReaction: &fakeosb.BindReaction{
			Error: osb.HTTPStatusCodeError{
				StatusCode:   http.StatusConflict,
				ErrorMessage: strPtr("ServiceInstanceCredentialExists"),
				Description:  strPtr("Service binding with the same id, for the same service instance already exists."),
			},
		},
	})

	sharedInformers.ClusterServiceBrokers().Informer().GetStore().Add(getTestClusterServiceBroker())
	sharedInformers.ServiceClasses().Informer().GetStore().Add(getTestServiceClass())
	sharedInformers.ServiceInstances().Informer().GetStore().Add(getTestServiceInstanceWithStatus(v1alpha1.ConditionTrue))
	sharedInformers.ServicePlans().Informer().GetStore().Add(getTestServicePlan())

	binding := getTestServiceInstanceCredential()

	if err := testController.reconcileServiceInstanceCredential(binding); err != nil {
		t.Fatalf("ServiceInstanceCredential creation should complete: %v", err)
	}

	// verify one kube action occurred
	kubeActions := fakeKubeClient.Actions()
	if err := checkKubeClientActions(kubeActions, []kubeClientAction{
		{verb: "get", resourceName: "namespaces", checkType: checkGetActionType},
	}); err != nil {
		t.Fatal(err)
	}

	actions := fakeCatalogClient.Actions()
	assertNumberOfActions(t, actions, 2)

	updatedServiceInstanceCredential := assertUpdateStatus(t, actions[0], binding)
	assertServiceInstanceCredentialOperationInProgress(t, updatedServiceInstanceCredential, v1alpha1.ServiceInstanceCredentialOperationBind, binding)
	assertServiceInstanceCredentialOrphanMitigationSet(t, updatedServiceInstanceCredential, false)

	updatedServiceInstanceCredential = assertUpdateStatus(t, actions[1], binding)
	assertServiceInstanceCredentialRequestFailingError(t, updatedServiceInstanceCredential, v1alpha1.ServiceInstanceCredentialOperationBind, errorBindCallReason, "ServiceInstanceCredentialReturnedFailure", binding)
	assertServiceInstanceCredentialOrphanMitigationSet(t, updatedServiceInstanceCredential, false)

	brokerActions := fakeClusterServiceBrokerClient.Actions()
	assertNumberOfClusterServiceBrokerActions(t, brokerActions, 1)
	assertBind(t, brokerActions[0], &osb.BindRequest{
		BindingID:  bindingGUID,
		InstanceID: instanceGUID,
		ServiceID:  serviceClassGUID,
		PlanID:     planGUID,
		AppGUID:    strPtr(""),
		BindResource: &osb.BindResource{
			AppGUID: strPtr(""),
		},
	})

	events := getRecordedEvents(testController)
	assertNumEvents(t, events, 1)

	expectedEvent := apiv1.EventTypeWarning + " " + errorBindCallReason + " " + "Error creating ServiceInstanceCredential \"test-binding/test-ns\" for ServiceInstance \"test-ns/test-instance\" of ServiceClass \"test-serviceclass\" at ClusterServiceBroker \"test-broker\", Status: 409; ErrorMessage: ServiceInstanceCredentialExists; Description: Service binding with the same id, for the same service instance already exists.; ResponseError: <nil>"

	if e, a := expectedEvent, events[0]; e != a {
		t.Fatalf("Received unexpected event: %v", a)
	}
}

// TestUpdateBindingCondition tests updateBindingCondition to ensure all status
// condition transitions on a binding work as expected.
//
// The test cases are proving:
// - a binding with no status that has status condition set to false will update
//   the transition time
// - a binding with condition false set to condition false will not update the
//   transition time
// - a binding with condition false set to condition false with a new message and
//   reason will not update the transition time
// - a binding with condition false set to condition true will update the
//   transition time
// - a binding with condition status true set to true will not update the
//   transition time
// - a binding with condition status true set to false will update the transition
//   time
func TestUpdateServiceInstanceCredentialCondition(t *testing.T) {
	getTestServiceInstanceCredentialWithStatus := func(status v1alpha1.ConditionStatus) *v1alpha1.ServiceInstanceCredential {
		instance := getTestServiceInstanceCredential()
		instance.Status = v1alpha1.ServiceInstanceCredentialStatus{
			Conditions: []v1alpha1.ServiceInstanceCredentialCondition{{
				Type:               v1alpha1.ServiceInstanceCredentialConditionReady,
				Status:             status,
				Message:            "message",
				LastTransitionTime: metav1.NewTime(time.Now().Add(-5 * time.Minute)),
			}},
		}

		return instance
	}

	// Anonymous struct fields:
	// name: short description of the test
	// input: the binding to test
	// status: condition status to set for binding condition
	// reason: reason to set for binding condition
	// message: message to set for binding condition
	// transitionTimeChanged: toggle for verifying transition time was updated
	cases := []struct {
		name                  string
		input                 *v1alpha1.ServiceInstanceCredential
		status                v1alpha1.ConditionStatus
		reason                string
		message               string
		transitionTimeChanged bool
	}{

		{
			name:                  "initially unset",
			input:                 getTestServiceInstanceCredential(),
			status:                v1alpha1.ConditionFalse,
			transitionTimeChanged: true,
		},
		{
			name:                  "not ready -> not ready",
			input:                 getTestServiceInstanceCredentialWithStatus(v1alpha1.ConditionFalse),
			status:                v1alpha1.ConditionFalse,
			transitionTimeChanged: false,
		},
		{
			name:                  "not ready -> not ready, message and reason change",
			input:                 getTestServiceInstanceCredentialWithStatus(v1alpha1.ConditionFalse),
			status:                v1alpha1.ConditionFalse,
			reason:                "foo",
			message:               "bar",
			transitionTimeChanged: false,
		},
		{
			name:                  "not ready -> ready",
			input:                 getTestServiceInstanceCredentialWithStatus(v1alpha1.ConditionFalse),
			status:                v1alpha1.ConditionTrue,
			transitionTimeChanged: true,
		},
		{
			name:                  "ready -> ready",
			input:                 getTestServiceInstanceCredentialWithStatus(v1alpha1.ConditionTrue),
			status:                v1alpha1.ConditionTrue,
			transitionTimeChanged: false,
		},
		{
			name:                  "ready -> not ready",
			input:                 getTestServiceInstanceCredentialWithStatus(v1alpha1.ConditionTrue),
			status:                v1alpha1.ConditionFalse,
			transitionTimeChanged: true,
		},
	}

	for _, tc := range cases {
		_, fakeCatalogClient, _, testController, _ := newTestController(t, noFakeActions())

		clone, err := api.Scheme.DeepCopy(tc.input)
		if err != nil {
			t.Errorf("%v: deep copy failed", tc.name)
			continue
		}
		inputClone := clone.(*v1alpha1.ServiceInstanceCredential)

		err = testController.updateServiceInstanceCredentialCondition(tc.input, v1alpha1.ServiceInstanceCredentialConditionReady, tc.status, tc.reason, tc.message)
		if err != nil {
			t.Errorf("%v: error updating broker condition: %v", tc.name, err)
			continue
		}

		if !reflect.DeepEqual(tc.input, inputClone) {
			t.Errorf("%v: updating broker condition mutated input: expected %v, got %v", tc.name, inputClone, tc.input)
			continue
		}

		actions := fakeCatalogClient.Actions()
		if ok := expectNumberOfActions(t, tc.name, actions, 1); !ok {
			continue
		}

		updatedServiceInstanceCredential, ok := expectUpdateStatus(t, tc.name, actions[0], tc.input)
		if !ok {
			continue
		}

		updateActionObject, ok := updatedServiceInstanceCredential.(*v1alpha1.ServiceInstanceCredential)
		if !ok {
			t.Errorf("%v: couldn't convert to binding", tc.name)
			continue
		}

		var initialTs metav1.Time
		if len(inputClone.Status.Conditions) != 0 {
			initialTs = inputClone.Status.Conditions[0].LastTransitionTime
		}

		if e, a := 1, len(updateActionObject.Status.Conditions); e != a {
			t.Errorf("%v: expected %v condition(s), got %v", tc.name, e, a)
		}

		outputCondition := updateActionObject.Status.Conditions[0]
		newTs := outputCondition.LastTransitionTime

		if tc.transitionTimeChanged && initialTs == newTs {
			t.Errorf("%v: transition time didn't change when it should have", tc.name)
			continue
		} else if !tc.transitionTimeChanged && initialTs != newTs {
			t.Errorf("%v: transition time changed when it shouldn't have", tc.name)
			continue
		}
		if e, a := tc.reason, outputCondition.Reason; e != "" && e != a {
			t.Errorf("%v: condition reasons didn't match; expected %v, got %v", tc.name, e, a)
			continue
		}
		if e, a := tc.message, outputCondition.Message; e != "" && e != a {
			t.Errorf("%v: condition reasons didn't match; expected %v, got %v", tc.name, e, a)
		}
	}
}

// TestReconcileUnbindingWithBrokerError tests reconcileBinding to ensure an
// unbinding request response that contains a broker error fails as expected.
func TestReconcileUnbindingWithClusterServiceBrokerError(t *testing.T) {
	_, fakeCatalogClient, _, testController, sharedInformers := newTestController(t, fakeosb.FakeClientConfiguration{
		UnbindReaction: &fakeosb.UnbindReaction{
			Response: &osb.UnbindResponse{},
			Error:    fakeosb.UnexpectedActionError(),
		},
	})

	sharedInformers.ClusterServiceBrokers().Informer().GetStore().Add(getTestClusterServiceBroker())
	sharedInformers.ServiceClasses().Informer().GetStore().Add(getTestServiceClass())
	sharedInformers.ServiceInstances().Informer().GetStore().Add(getTestServiceInstanceWithStatus(v1alpha1.ConditionTrue))
	sharedInformers.ServicePlans().Informer().GetStore().Add(getTestServicePlan())

	t1 := metav1.NewTime(time.Now())
	binding := &v1alpha1.ServiceInstanceCredential{
		ObjectMeta: metav1.ObjectMeta{
			Name:              testServiceInstanceCredentialName,
			Namespace:         testNamespace,
			DeletionTimestamp: &t1,
			Generation:        1,
		},
		Spec: v1alpha1.ServiceInstanceCredentialSpec{
			ServiceInstanceRef: v1.LocalObjectReference{Name: testServiceInstanceName},
			ExternalID:         bindingGUID,
			SecretName:         testServiceInstanceCredentialSecretName,
		},
		Status: v1alpha1.ServiceInstanceCredentialStatus{
			ExternalProperties: &v1alpha1.ServiceInstanceCredentialPropertiesState{},
		},
	}
	if err := scmeta.AddFinalizer(binding, v1alpha1.FinalizerServiceCatalog); err != nil {
		t.Fatalf("Finalizer error: %v", err)
	}
	if err := testController.reconcileServiceInstanceCredential(binding); err == nil {
		t.Fatal("reconcileServiceInstanceCredential should have returned an error")
	}

	actions := fakeCatalogClient.Actions()
	assertNumberOfActions(t, actions, 2)

	updatedServiceInstanceCredential := assertUpdateStatus(t, actions[0], binding)
	assertServiceInstanceCredentialOperationInProgress(t, updatedServiceInstanceCredential, v1alpha1.ServiceInstanceCredentialOperationUnbind, binding)
	assertServiceInstanceCredentialOrphanMitigationSet(t, updatedServiceInstanceCredential, false)

	updatedServiceInstanceCredential = assertUpdateStatus(t, actions[1], binding)
	assertServiceInstanceCredentialRequestRetriableError(t, updatedServiceInstanceCredential, v1alpha1.ServiceInstanceCredentialOperationUnbind, errorUnbindCallReason, binding)
	assertServiceInstanceCredentialOrphanMitigationSet(t, updatedServiceInstanceCredential, false)

	events := getRecordedEvents(testController)
	expectedEvent := apiv1.EventTypeWarning + " " + errorUnbindCallReason + " " + `Error unbinding ServiceInstanceCredential "test-ns/test-binding" for ServiceInstance "test-ns/test-instance" of ServiceClass "test-serviceclass" at ClusterServiceBroker "test-broker": Unexpected action`
	if 1 != len(events) {
		t.Fatalf("Did not record expected event, expecting: %v", expectedEvent)
	}
	if e, a := expectedEvent, events[0]; e != a {
		t.Fatalf("Received unexpected event: %v, expecting: %v", a, e)
	}
}

// TestReconcileUnbindingWithClusterServiceBrokerHTTPError tests reconcileBinding to ensure an
// unbinding request response that contains a broker HTTP error fails as
// expected.
func TestReconcileUnbindingWithClusterServiceBrokerHTTPError(t *testing.T) {
	_, fakeCatalogClient, _, testController, sharedInformers := newTestController(t, fakeosb.FakeClientConfiguration{
		UnbindReaction: &fakeosb.UnbindReaction{
			Response: &osb.UnbindResponse{},
			Error: osb.HTTPStatusCodeError{
				StatusCode: http.StatusGone,
			},
		},
	})

	sharedInformers.ClusterServiceBrokers().Informer().GetStore().Add(getTestClusterServiceBroker())
	sharedInformers.ServiceClasses().Informer().GetStore().Add(getTestServiceClass())
	sharedInformers.ServiceInstances().Informer().GetStore().Add(getTestServiceInstanceWithStatus(v1alpha1.ConditionTrue))
	sharedInformers.ServicePlans().Informer().GetStore().Add(getTestServicePlan())

	t1 := metav1.NewTime(time.Now())
	binding := &v1alpha1.ServiceInstanceCredential{
		ObjectMeta: metav1.ObjectMeta{
			Name:              testServiceInstanceCredentialName,
			Namespace:         testNamespace,
			DeletionTimestamp: &t1,
			Generation:        1,
		},
		Spec: v1alpha1.ServiceInstanceCredentialSpec{
			ServiceInstanceRef: v1.LocalObjectReference{Name: testServiceInstanceName},
			ExternalID:         bindingGUID,
			SecretName:         testServiceInstanceCredentialSecretName,
		},
		Status: v1alpha1.ServiceInstanceCredentialStatus{
			ExternalProperties: &v1alpha1.ServiceInstanceCredentialPropertiesState{},
		},
	}
	if err := scmeta.AddFinalizer(binding, v1alpha1.FinalizerServiceCatalog); err != nil {
		t.Fatalf("Finalizer error: %v", err)
	}
	if err := testController.reconcileServiceInstanceCredential(binding); err != nil {
		t.Fatalf("reconcileServiceInstanceCredential should not have returned an error: %v", err)
	}

	actions := fakeCatalogClient.Actions()
	assertNumberOfActions(t, actions, 2)

	updatedServiceInstanceCredential := assertUpdateStatus(t, actions[0], binding)
	assertServiceInstanceCredentialOperationInProgress(t, updatedServiceInstanceCredential, v1alpha1.ServiceInstanceCredentialOperationUnbind, binding)
	assertServiceInstanceCredentialOrphanMitigationSet(t, updatedServiceInstanceCredential, false)

	updatedServiceInstanceCredential = assertUpdateStatus(t, actions[1], binding)
	assertServiceInstanceCredentialRequestFailingError(t, updatedServiceInstanceCredential, v1alpha1.ServiceInstanceCredentialOperationUnbind, errorUnbindCallReason, errorUnbindCallReason, binding)
	assertServiceInstanceCredentialOrphanMitigationSet(t, updatedServiceInstanceCredential, false)

	events := getRecordedEvents(testController)

	expectedEvent := apiv1.EventTypeWarning + " " + errorUnbindCallReason + " " + `Error unbinding ServiceInstanceCredential "test-binding/test-ns" for ServiceInstance "test-ns/test-instance" of ServiceClass "test-serviceclass" at ClusterServiceBroker "test-broker": Status: 410; ErrorMessage: <nil>; Description: <nil>; ResponseError: <nil>`
	if 1 != len(events) {
		t.Fatalf("Did not record expected event, expecting: %v", expectedEvent)
	}
	if e, a := expectedEvent, events[0]; e != a {
		t.Fatalf("Received unexpected event: %v, expecting: %v", a, e)
	}
}

func TestReconcileBindingUsingOriginatingIdentity(t *testing.T) {
	for _, tc := range originatingIdentityTestCases {
		func() {
			if tc.enableOriginatingIdentity {
				utilfeature.DefaultFeatureGate.Set(fmt.Sprintf("%v=true", scfeatures.OriginatingIdentity))
				defer utilfeature.DefaultFeatureGate.Set(fmt.Sprintf("%v=false", scfeatures.OriginatingIdentity))
			}

			fakeKubeClient, _, fakeBrokerClient, testController, sharedInformers := newTestController(t, fakeosb.FakeClientConfiguration{
				BindReaction: &fakeosb.BindReaction{
					Response: &osb.BindResponse{},
				},
			})

			addGetNamespaceReaction(fakeKubeClient)
			addGetSecretNotFoundReaction(fakeKubeClient)

			sharedInformers.ClusterServiceBrokers().Informer().GetStore().Add(getTestClusterServiceBroker())
			sharedInformers.ServiceClasses().Informer().GetStore().Add(getTestServiceClass())
			sharedInformers.ServicePlans().Informer().GetStore().Add(getTestServicePlan())
			sharedInformers.ServiceInstances().Informer().GetStore().Add(getTestServiceInstanceWithStatus(v1alpha1.ConditionTrue))

			binding := getTestServiceInstanceCredential()
			if tc.includeUserInfo {
				binding.Spec.UserInfo = testUserInfo
			}

			err := testController.reconcileServiceInstanceCredential(binding)
			if err != nil {
				t.Fatalf("%v: a valid binding should not fail: %v", tc.name, err)
			}

			brokerActions := fakeBrokerClient.Actions()
			assertNumberOfClusterServiceBrokerActions(t, brokerActions, 1)
			actualRequest, ok := brokerActions[0].Request.(*osb.BindRequest)
			if !ok {
				t.Errorf("%v: unexpected request type; expected %T, got %T", tc.name, &osb.BindRequest{}, actualRequest)
				return
			}
			var expectedOriginatingIdentity *osb.AlphaOriginatingIdentity
			if tc.expectedOriginatingIdentity {
				expectedOriginatingIdentity = testOriginatingIdentity
			}
			assertOriginatingIdentity(t, expectedOriginatingIdentity, actualRequest.OriginatingIdentity)
		}()
	}
}

func TestReconcileBindingDeleteUsingOriginatingIdentity(t *testing.T) {
	for _, tc := range originatingIdentityTestCases {
		func() {
			if tc.enableOriginatingIdentity {
				err := utilfeature.DefaultFeatureGate.Set(fmt.Sprintf("%v=true", scfeatures.OriginatingIdentity))
				if err != nil {
					t.Fatalf("Failed to enable originating identity feature: %v", err)
				}
				defer utilfeature.DefaultFeatureGate.Set(fmt.Sprintf("%v=false", scfeatures.OriginatingIdentity))
			}

			fakeKubeClient, _, fakeBrokerClient, testController, sharedInformers := newTestController(t, fakeosb.FakeClientConfiguration{
				UnbindReaction: &fakeosb.UnbindReaction{},
			})

			addGetNamespaceReaction(fakeKubeClient)
			addGetSecretNotFoundReaction(fakeKubeClient)

			sharedInformers.ClusterServiceBrokers().Informer().GetStore().Add(getTestClusterServiceBroker())
			sharedInformers.ServiceClasses().Informer().GetStore().Add(getTestServiceClass())
			sharedInformers.ServicePlans().Informer().GetStore().Add(getTestServicePlan())
			sharedInformers.ServiceInstances().Informer().GetStore().Add(getTestServiceInstanceWithStatus(v1alpha1.ConditionTrue))

			binding := getTestServiceInstanceCredential()
			binding.DeletionTimestamp = &metav1.Time{}
			binding.Finalizers = []string{v1alpha1.FinalizerServiceCatalog}
			if tc.includeUserInfo {
				binding.Spec.UserInfo = testUserInfo
			}

			err := testController.reconcileServiceInstanceCredential(binding)
			if err != nil {
				t.Fatalf("%v: a valid binding should not fail: %v", tc.name, err)
			}

			brokerActions := fakeBrokerClient.Actions()
			assertNumberOfClusterServiceBrokerActions(t, brokerActions, 1)
			actualRequest, ok := brokerActions[0].Request.(*osb.UnbindRequest)
			if !ok {
				t.Errorf("%v: unexpected request type; expected %T, got %T", tc.name, &osb.UnbindRequest{}, actualRequest)
				return
			}
			var expectedOriginatingIdentity *osb.AlphaOriginatingIdentity
			if tc.expectedOriginatingIdentity {
				expectedOriginatingIdentity = testOriginatingIdentity
			}
			assertOriginatingIdentity(t, expectedOriginatingIdentity, actualRequest.OriginatingIdentity)
		}()
	}
}

// TestReconcileBindingSuccessOnFinalRetry verifies that reconciliation can
// succeed on the last attempt before timing out of the retry loop
func TestReconcileBindingSuccessOnFinalRetry(t *testing.T) {
	fakeKubeClient, fakeCatalogClient, fakeClusterServiceBrokerClient, testController, sharedInformers := newTestController(t, fakeosb.FakeClientConfiguration{
		BindReaction: &fakeosb.BindReaction{
			Response: &osb.BindResponse{
				Credentials: map[string]interface{}{
					"a": "b",
					"c": "d",
				},
			},
		},
	})

	addGetNamespaceReaction(fakeKubeClient)
	addGetSecretNotFoundReaction(fakeKubeClient)

	sharedInformers.ClusterServiceBrokers().Informer().GetStore().Add(getTestClusterServiceBroker())
	sharedInformers.ServiceClasses().Informer().GetStore().Add(getTestServiceClass())
	sharedInformers.ServicePlans().Informer().GetStore().Add(getTestServicePlan())
	sharedInformers.ServiceInstances().Informer().GetStore().Add(getTestServiceInstanceWithStatus(v1alpha1.ConditionTrue))

	binding := getTestServiceInstanceCredential()
	binding.Status.CurrentOperation = v1alpha1.ServiceInstanceCredentialOperationBind
	startTime := metav1.NewTime(time.Now().Add(-7 * 24 * time.Hour))
	binding.Status.OperationStartTime = &startTime

	if err := testController.reconcileServiceInstanceCredential(binding); err != nil {
		t.Fatalf("a valid binding should not fail: %v", err)
	}

	brokerActions := fakeClusterServiceBrokerClient.Actions()
	assertNumberOfClusterServiceBrokerActions(t, brokerActions, 1)
	assertBind(t, brokerActions[0], &osb.BindRequest{
		BindingID:  bindingGUID,
		InstanceID: instanceGUID,
		ServiceID:  serviceClassGUID,
		PlanID:     planGUID,
		AppGUID:    strPtr(testNsUID),
		BindResource: &osb.BindResource{
			AppGUID: strPtr(testNsUID),
		},
	})

	actions := fakeCatalogClient.Actions()
	assertNumberOfActions(t, actions, 1)

	updatedServiceInstanceCredential := assertUpdateStatus(t, actions[0], binding).(*v1alpha1.ServiceInstanceCredential)
	assertServiceInstanceCredentialOperationSuccess(t, updatedServiceInstanceCredential, v1alpha1.ServiceInstanceCredentialOperationBind, binding)
	assertServiceInstanceCredentialOrphanMitigationSet(t, updatedServiceInstanceCredential, false)

	events := getRecordedEvents(testController)
	assertNumEvents(t, events, 1)

	expectedEvent := apiv1.EventTypeNormal + " " + successInjectedBindResultReason + " " + successInjectedBindResultMessage
	if e, a := expectedEvent, events[0]; e != a {
		t.Fatalf("Received unexpected event: %v", a)
	}
}

// TestReconcileBindingFailureOnFinalRetry verifies that reconciliation
// completes in the event of an error after the retry duration elapses.
func TestReconcileBindingFailureOnFinalRetry(t *testing.T) {
	_, fakeCatalogClient, _, testController, sharedInformers := newTestController(t, fakeosb.FakeClientConfiguration{
		BindReaction: &fakeosb.BindReaction{
			Response: &osb.BindResponse{
				Credentials: map[string]interface{}{
					"a": "b",
					"c": "d",
				},
			},
			Error: fakeosb.UnexpectedActionError(),
		},
	})

	sharedInformers.ClusterServiceBrokers().Informer().GetStore().Add(getTestClusterServiceBroker())
	sharedInformers.ServiceClasses().Informer().GetStore().Add(getTestServiceClass())
	sharedInformers.ServicePlans().Informer().GetStore().Add(getTestServicePlan())
	sharedInformers.ServiceInstances().Informer().GetStore().Add(getTestServiceInstanceWithStatus(v1alpha1.ConditionTrue))

	binding := getTestServiceInstanceCredential()
	binding.Status.CurrentOperation = v1alpha1.ServiceInstanceCredentialOperationBind
	startTime := metav1.NewTime(time.Now().Add(-7 * 24 * time.Hour))
	binding.Status.OperationStartTime = &startTime

	if err := testController.reconcileServiceInstanceCredential(binding); err != nil {
		t.Fatalf("Should have return no error because the retry duration has elapsed: %v", err)
	}

	actions := fakeCatalogClient.Actions()
	assertNumberOfActions(t, actions, 1)

	updatedServiceInstanceCredential := assertUpdateStatus(t, actions[0], binding).(*v1alpha1.ServiceInstanceCredential)
	assertServiceInstanceCredentialRequestFailingError(t, updatedServiceInstanceCredential, v1alpha1.ServiceInstanceCredentialOperationBind, errorBindCallReason, errorReconciliationRetryTimeoutReason, binding)
	assertServiceInstanceCredentialOrphanMitigationSet(t, updatedServiceInstanceCredential, false)

	expectedEventPrefixes := []string{
		apiv1.EventTypeWarning + " " + errorBindCallReason,
		apiv1.EventTypeWarning + " " + errorReconciliationRetryTimeoutReason,
	}
	events := getRecordedEvents(testController)
	assertNumEvents(t, events, len(expectedEventPrefixes))

	for i, e := range expectedEventPrefixes {
		a := events[i]
		if !strings.HasPrefix(a, e) {
			t.Fatalf("Received unexpected event:\n  expected prefix: %v\n  got: %v", e, a)
		}
	}
}

// TestReconcileBindingWithSecretConflictFailedAfterFinalRetry tests
// reconcileBinding to ensure a binding with an existing secret not owned by the
// bindings is marked as failed after the retry duration elapses.
func TestReconcileBindingWithSecretConflictFailedAfterFinalRetry(t *testing.T) {
	fakeKubeClient, fakeCatalogClient, fakeClusterServiceBrokerClient, testController, sharedInformers := newTestController(t, fakeosb.FakeClientConfiguration{
		BindReaction: &fakeosb.BindReaction{
			Response: &osb.BindResponse{
				Credentials: map[string]interface{}{
					"a": "b",
					"c": "d",
				},
			},
		},
	})

	addGetNamespaceReaction(fakeKubeClient)
	// existing Secret with nil controllerRef
	addGetSecretReaction(fakeKubeClient, &v1.Secret{
		ObjectMeta: metav1.ObjectMeta{Name: testServiceInstanceCredentialName, Namespace: testNamespace},
	})

	sharedInformers.ClusterServiceBrokers().Informer().GetStore().Add(getTestClusterServiceBroker())
	sharedInformers.ServiceClasses().Informer().GetStore().Add(getTestServiceClass())
	sharedInformers.ServicePlans().Informer().GetStore().Add(getTestServicePlan())
	sharedInformers.ServiceInstances().Informer().GetStore().Add(getTestServiceInstanceWithStatus(v1alpha1.ConditionTrue))

	startTime := metav1.NewTime(time.Now().Add(-7 * 24 * time.Hour))
	binding := &v1alpha1.ServiceInstanceCredential{
		ObjectMeta: metav1.ObjectMeta{
			Name:       testServiceInstanceCredentialName,
			Namespace:  testNamespace,
			Generation: 1,
		},
		Spec: v1alpha1.ServiceInstanceCredentialSpec{
			ServiceInstanceRef: v1.LocalObjectReference{Name: testServiceInstanceName},
			ExternalID:         bindingGUID,
			SecretName:         testServiceInstanceCredentialSecretName,
		},
		Status: v1alpha1.ServiceInstanceCredentialStatus{
			CurrentOperation:   v1alpha1.ServiceInstanceCredentialOperationBind,
			OperationStartTime: &startTime,
		},
	}

	if err := testController.reconcileServiceInstanceCredential(binding); err != nil {
		t.Fatalf("reconciliation should complete since the retry duration has elapsed: %v", err)
	}

	brokerActions := fakeClusterServiceBrokerClient.Actions()
	assertNumberOfClusterServiceBrokerActions(t, brokerActions, 1)
	assertBind(t, brokerActions[0], &osb.BindRequest{
		BindingID:  bindingGUID,
		InstanceID: instanceGUID,
		ServiceID:  serviceClassGUID,
		PlanID:     planGUID,
		AppGUID:    strPtr(testNsUID),
		BindResource: &osb.BindResource{
			AppGUID: strPtr(testNsUID),
		},
	})

	actions := fakeCatalogClient.Actions()
	assertNumberOfActions(t, actions, 1)

	updatedServiceInstanceCredential := assertUpdateStatus(t, actions[0], binding).(*v1alpha1.ServiceInstanceCredential)
	assertServiceInstanceCredentialReadyFalse(t, updatedServiceInstanceCredential, errorServiceInstanceCredentialOrphanMitigation)
	assertServiceInstanceCredentialCondition(t, updatedServiceInstanceCredential, v1alpha1.ServiceInstanceCredentialConditionFailed, v1alpha1.ConditionTrue, errorReconciliationRetryTimeoutReason)
	assertServiceInstanceCredentialCurrentOperation(t, updatedServiceInstanceCredential, v1alpha1.ServiceInstanceCredentialOperationBind)
	assertServiceInstanceCredentialOperationStartTimeSet(t, updatedServiceInstanceCredential, false)
	assertServiceInstanceCredentialReconciledGeneration(t, updatedServiceInstanceCredential, binding.Status.ReconciledGeneration)
	assertServiceInstanceCredentialInProgressPropertiesNil(t, updatedServiceInstanceCredential)
	// External properties are updated because the bind request with the Broker was successful
	assertServiceInstanceCredentialExternalPropertiesParameters(t, updatedServiceInstanceCredential, nil, "")
	assertServiceInstanceCredentialCondition(t, updatedServiceInstanceCredential, v1alpha1.ServiceInstanceCredentialConditionReady, v1alpha1.ConditionFalse, errorServiceInstanceCredentialOrphanMitigation)
	assertServiceInstanceCredentialOrphanMitigationSet(t, updatedServiceInstanceCredential, true)

	kubeActions := fakeKubeClient.Actions()
	assertNumberOfActions(t, kubeActions, 2)

	// first action is a get on the namespace
	// second action is a get on the secret
	action := kubeActions[1].(clientgotesting.GetAction)
	if e, a := "get", action.GetVerb(); e != a {
		t.Fatalf("Unexpected verb on action; expected %v, got %v", e, a)
	}
	if e, a := "secrets", action.GetResource().Resource; e != a {
		t.Fatalf("Unexpected resource on action; expected %v, got %v", e, a)
	}

	expectedEventPrefixes := []string{
		apiv1.EventTypeWarning + " " + errorInjectingBindResultReason,
		apiv1.EventTypeWarning + " " + errorReconciliationRetryTimeoutReason,
		apiv1.EventTypeWarning + " " + errorServiceInstanceCredentialOrphanMitigation,
	}
	events := getRecordedEvents(testController)
	assertNumEvents(t, events, len(expectedEventPrefixes))
	for i, e := range expectedEventPrefixes {
		if a := events[i]; !strings.HasPrefix(a, e) {
			t.Fatalf("Received unexpected event:\n  expected prefix: %v\n  got: %v", e, a)
		}
	}
}

// TestReconcileServiceInstanceCredentialWithStatusUpdateError verifies that the
// reconciler returns an error when there is a conflict updating the status of
// the resource. This is an otherwise successful scenario where the update to set
// the in-progress operation fails.
func TestReconcileServiceInstanceCredentialWithStatusUpdateError(t *testing.T) {
	fakeKubeClient, fakeCatalogClient, fakeClusterServiceBrokerClient, testController, sharedInformers := newTestController(t, noFakeActions())

	addGetNamespaceReaction(fakeKubeClient)
	addGetSecretNotFoundReaction(fakeKubeClient)

	sharedInformers.ClusterServiceBrokers().Informer().GetStore().Add(getTestClusterServiceBroker())
	sharedInformers.ServiceClasses().Informer().GetStore().Add(getTestServiceClass())
	sharedInformers.ServicePlans().Informer().GetStore().Add(getTestServicePlan())
	sharedInformers.ServiceInstances().Informer().GetStore().Add(getTestServiceInstanceWithStatus(v1alpha1.ConditionTrue))

	binding := getTestServiceInstanceCredential()

	fakeCatalogClient.AddReactor("update", "serviceinstancecredentials", func(action clientgotesting.Action) (bool, runtime.Object, error) {
		return true, nil, errors.New("update error")
	})

	err := testController.reconcileServiceInstanceCredential(binding)
	if err == nil {
		t.Fatalf("expected error from but got none")
	}
	if e, a := "update error", err.Error(); e != a {
		t.Fatalf("unexpected error returned: expected %q, got %q", e, a)
	}

	brokerActions := fakeClusterServiceBrokerClient.Actions()
	assertNumberOfClusterServiceBrokerActions(t, brokerActions, 0)

	actions := fakeCatalogClient.Actions()
	assertNumberOfActions(t, actions, 1)

	updatedServiceInstanceCredential := assertUpdateStatus(t, actions[0], binding)
	assertServiceInstanceCredentialOperationInProgress(t, updatedServiceInstanceCredential, v1alpha1.ServiceInstanceCredentialOperationBind, binding)
	assertServiceInstanceCredentialOrphanMitigationSet(t, updatedServiceInstanceCredential, false)

	events := getRecordedEvents(testController)
	assertNumEvents(t, events, 0)
}

// TestReconcileServiceInstanceCredentailWithSecretParameters tests reconciling a
// binding that has parameters obtained from secrets.
func TestReconcileServiceInstanceCredentialWithSecretParameters(t *testing.T) {
	fakeKubeClient, fakeCatalogClient, fakeClusterServiceBrokerClient, testController, sharedInformers := newTestController(t, fakeosb.FakeClientConfiguration{
		BindReaction: &fakeosb.BindReaction{
			Response: &osb.BindResponse{
				Credentials: map[string]interface{}{
					"a": "b",
					"c": "d",
				},
			},
		},
	})

	addGetNamespaceReaction(fakeKubeClient)

	paramSecret := &v1.Secret{
		Data: map[string][]byte{
			"param-secret-key": []byte("{\"b\":\"2\"}"),
		},
	}
	fakeKubeClient.AddReactor("get", "secrets", func(action clientgotesting.Action) (bool, runtime.Object, error) {
		switch name := action.(clientgotesting.GetAction).GetName(); name {
		case "param-secret-name":
			return true, paramSecret, nil
		default:
			return true, nil, apierrors.NewNotFound(action.GetResource().GroupResource(), name)
		}
	})

	sharedInformers.ClusterServiceBrokers().Informer().GetStore().Add(getTestClusterServiceBroker())
	sharedInformers.ServiceClasses().Informer().GetStore().Add(getTestServiceClass())
	sharedInformers.ServicePlans().Informer().GetStore().Add(getTestServicePlan())
	sharedInformers.ServiceInstances().Informer().GetStore().Add(getTestServiceInstanceWithStatus(v1alpha1.ConditionTrue))

	binding := &v1alpha1.ServiceInstanceCredential{
		ObjectMeta: metav1.ObjectMeta{
			Name:       testServiceInstanceCredentialName,
			Namespace:  testNamespace,
			Generation: 1,
		},
		Spec: v1alpha1.ServiceInstanceCredentialSpec{
			ServiceInstanceRef: v1.LocalObjectReference{Name: testServiceInstanceName},
			ExternalID:         bindingGUID,
			SecretName:         testServiceInstanceCredentialSecretName,
		},
	}

	parameters := map[string]interface{}{
		"a": "1",
	}
	b, err := json.Marshal(parameters)
	if err != nil {
		t.Fatalf("Failed to marshal parameters %v : %v", parameters, err)
	}
	binding.Spec.Parameters = &runtime.RawExtension{Raw: b}

	binding.Spec.ParametersFrom = []v1alpha1.ParametersFromSource{
		{
			SecretKeyRef: &v1alpha1.SecretKeyReference{
				Name: "param-secret-name",
				Key:  "param-secret-key",
			},
		},
	}

	err = testController.reconcileServiceInstanceCredential(binding)
	if err != nil {
		t.Fatalf("a valid binding should not fail: %v", err)
	}

	brokerActions := fakeClusterServiceBrokerClient.Actions()
	assertNumberOfClusterServiceBrokerActions(t, brokerActions, 1)
	assertBind(t, brokerActions[0], &osb.BindRequest{
		BindingID:  bindingGUID,
		InstanceID: instanceGUID,
		ServiceID:  serviceClassGUID,
		PlanID:     planGUID,
		AppGUID:    strPtr(testNsUID),
		Parameters: map[string]interface{}{
			"a": "1",
			"b": "2",
		},
		BindResource: &osb.BindResource{
			AppGUID: strPtr(testNsUID),
		},
	})

	expectedParameters := map[string]interface{}{
		"a": "1",
		"b": "<redacted>",
	}
	expectedParametersChecksum, err := generateChecksumOfParameters(map[string]interface{}{
		"a": "1",
		"b": "2",
	})
	if err != nil {
		t.Fatalf("Failed to generate parameters checksum: %v", err)
	}

	actions := fakeCatalogClient.Actions()
	assertNumberOfActions(t, actions, 2)

	updatedServiceInstanceCredential := assertUpdateStatus(t, actions[0], binding)
	assertServiceInstanceCredentialOperationInProgressWithParameters(t, updatedServiceInstanceCredential, v1alpha1.ServiceInstanceCredentialOperationBind, expectedParameters, expectedParametersChecksum, binding)
	assertServiceInstanceCredentialOrphanMitigationSet(t, updatedServiceInstanceCredential, false)

	updatedServiceInstanceCredential = assertUpdateStatus(t, actions[1], binding)
	assertServiceInstanceCredentialOperationSuccessWithParameters(t, updatedServiceInstanceCredential, v1alpha1.ServiceInstanceCredentialOperationBind, expectedParameters, expectedParametersChecksum, binding)
	assertServiceInstanceCredentialOrphanMitigationSet(t, updatedServiceInstanceCredential, false)

	kubeActions := fakeKubeClient.Actions()
	assertNumberOfActions(t, kubeActions, 4)

	// first action is a get on the namespace
	// second action is a get on the secret, to build the parameters
	action, ok := kubeActions[1].(clientgotesting.GetAction)
	if !ok {
		t.Fatalf("unexpected type of action: expected a GetAction, got %T", kubeActions[0])
	}
	if e, a := "secrets", action.GetResource().Resource; e != a {
		t.Fatalf("Unexpected resource on action: expected %q, got %q", e, a)
	}
	if e, a := "param-secret-name", action.GetName(); e != a {
		t.Fatalf("Unexpected name of secret fetched: expected %q, got %q", e, a)
	}

	events := getRecordedEvents(testController)
	assertNumEvents(t, events, 1)

	expectedEvent := apiv1.EventTypeNormal + " " + successInjectedBindResultReason + " " + successInjectedBindResultMessage
	if e, a := expectedEvent, events[0]; e != a {
		t.Fatalf("Received unexpected event: %v", a)
	}
}

// TestReconcileBindingWithSetOrphanMitigation tests
// reconcileServiceInstanceCredential to ensure a binding properly initiates
// orphan mitigation in the case of timeout or receiving certain HTTP codes.
func TestReconcileBindingWithSetOrphanMitigation(t *testing.T) {
	// Anonymous struct fields:
	// bindReactionError: the error to return from the bind attempt
	// setOrphanMitigation: flag for whether or not orphan migitation
	//                      should be performed
	cases := []struct {
		bindReactionError   error
		setOrphanMitigation bool
		shouldReturnError   bool
	}{
		{
			bindReactionError:   testTimeoutError{},
			setOrphanMitigation: false,
			shouldReturnError:   true,
		},
		{
			bindReactionError: osb.HTTPStatusCodeError{
				StatusCode: 200,
			},
			setOrphanMitigation: false,
			shouldReturnError:   false,
		},
		{
			bindReactionError: osb.HTTPStatusCodeError{
				StatusCode: 201,
			},
			setOrphanMitigation: true,
			shouldReturnError:   false,
		},
		{
			bindReactionError: osb.HTTPStatusCodeError{
				StatusCode: 300,
			},
			setOrphanMitigation: false,
			shouldReturnError:   false,
		},
		{
			bindReactionError: osb.HTTPStatusCodeError{
				StatusCode: 400,
			},
			setOrphanMitigation: false,
			shouldReturnError:   false,
		},
		{
			bindReactionError: osb.HTTPStatusCodeError{
				StatusCode: 408,
			},
			setOrphanMitigation: true,
			shouldReturnError:   false,
		},
		{
			bindReactionError: osb.HTTPStatusCodeError{
				StatusCode: 500,
			},
			setOrphanMitigation: true,
			shouldReturnError:   false,
		},
		{
			bindReactionError: osb.HTTPStatusCodeError{
				StatusCode: 501,
			},
			setOrphanMitigation: true,
			shouldReturnError:   false,
		},
	}

	for _, tc := range cases {
		fakeKubeClient, fakeCatalogClient, fakeServiceBrokerClient, testController, sharedInformers := newTestController(t, fakeosb.FakeClientConfiguration{
			BindReaction: &fakeosb.BindReaction{
				Response: &osb.BindResponse{},
				Error:    tc.bindReactionError,
			},
		})

		addGetNamespaceReaction(fakeKubeClient)
		// existing Secret with nil controllerRef
		addGetSecretReaction(fakeKubeClient, &v1.Secret{
			ObjectMeta: metav1.ObjectMeta{Name: testServiceInstanceCredentialName, Namespace: testNamespace},
		})

		sharedInformers.ClusterServiceBrokers().Informer().GetStore().Add(getTestClusterServiceBroker())
		sharedInformers.ServiceClasses().Informer().GetStore().Add(getTestServiceClass())
		sharedInformers.ServicePlans().Informer().GetStore().Add(getTestServicePlan())
		sharedInformers.ServiceInstances().Informer().GetStore().Add(getTestServiceInstanceWithStatus(v1alpha1.ConditionTrue))

		binding := &v1alpha1.ServiceInstanceCredential{
			ObjectMeta: metav1.ObjectMeta{
				Name:       testServiceInstanceCredentialName,
				Namespace:  testNamespace,
				Generation: 1,
			},
			Spec: v1alpha1.ServiceInstanceCredentialSpec{
				ServiceInstanceRef: v1.LocalObjectReference{Name: testServiceInstanceName},
				ExternalID:         bindingGUID,
				SecretName:         testServiceInstanceCredentialSecretName,
			},
		}
		startTime := metav1.NewTime(time.Now().Add(-7 * 24 * time.Hour))
		binding.Status.OperationStartTime = &startTime

		if err := testController.reconcileServiceInstanceCredential(binding); tc.shouldReturnError && err == nil || !tc.shouldReturnError && err != nil {
			t.Fatalf("expected to return %v from reconciliation attempt, got %v", tc.shouldReturnError, err)
		}

		brokerActions := fakeServiceBrokerClient.Actions()
		assertNumberOfClusterServiceBrokerActions(t, brokerActions, 1)
		assertBind(t, brokerActions[0], &osb.BindRequest{
			BindingID:  bindingGUID,
			InstanceID: instanceGUID,
			ServiceID:  serviceClassGUID,
			PlanID:     planGUID,
			AppGUID:    strPtr(testNsUID),
			BindResource: &osb.BindResource{
				AppGUID: strPtr(testNsUID),
			},
		})

		kubeActions := fakeKubeClient.Actions()
		assertNumberOfActions(t, kubeActions, 1)
		action := kubeActions[0].(clientgotesting.GetAction)
		if e, a := "get", action.GetVerb(); e != a {
			t.Fatalf("Unexpected verb on action; expected %v, got %v", e, a)
		}
		if e, a := "namespaces", action.GetResource().Resource; e != a {
			t.Fatalf("Unexpected resource on action; expected %v, got %v", e, a)
		}

		actions := fakeCatalogClient.Actions()
		assertNumberOfActions(t, actions, 2)

		updatedServiceInstanceCredential := assertUpdateStatus(t, actions[0], binding).(*v1alpha1.ServiceInstanceCredential)
		assertServiceInstanceCredentialReadyFalse(t, updatedServiceInstanceCredential)

		updatedServiceInstanceCredential = assertUpdateStatus(t, actions[1], binding).(*v1alpha1.ServiceInstanceCredential)
		assertServiceInstanceCredentialReadyFalse(t, updatedServiceInstanceCredential)
		assertServiceInstanceCredentialCondition(t, updatedServiceInstanceCredential, v1alpha1.ServiceInstanceCredentialConditionReady, v1alpha1.ConditionFalse)

		assertServiceInstanceCredentialOrphanMitigationSet(t, updatedServiceInstanceCredential, tc.setOrphanMitigation)
	}
}

// TestReconcileBindingWithOrphanMitigationInProgress tests
// reconcileServiceInstanceCredential to ensure a binding is properly handled
// once orphan mitigation is underway.
func TestReconcileBindingWithOrphanMitigationInProgress(t *testing.T) {
	fakeKubeClient, fakeCatalogClient, fakeServiceBrokerClient, testController, sharedInformers := newTestController(t, fakeosb.FakeClientConfiguration{
		UnbindReaction: &fakeosb.UnbindReaction{},
	})

	addGetNamespaceReaction(fakeKubeClient)
	// existing Secret with nil controllerRef
	addGetSecretReaction(fakeKubeClient, &v1.Secret{
		ObjectMeta: metav1.ObjectMeta{Name: testServiceInstanceCredentialName, Namespace: testNamespace},
	})

	sharedInformers.ClusterServiceBrokers().Informer().GetStore().Add(getTestClusterServiceBroker())
	sharedInformers.ServiceClasses().Informer().GetStore().Add(getTestServiceClass())
	sharedInformers.ServicePlans().Informer().GetStore().Add(getTestServicePlan())
	sharedInformers.ServiceInstances().Informer().GetStore().Add(getTestServiceInstanceWithStatus(v1alpha1.ConditionTrue))

	binding := &v1alpha1.ServiceInstanceCredential{
		ObjectMeta: metav1.ObjectMeta{
			Name:       testServiceInstanceCredentialName,
			Namespace:  testNamespace,
			Finalizers: []string{v1alpha1.FinalizerServiceCatalog},
			Generation: 1,
		},
		Spec: v1alpha1.ServiceInstanceCredentialSpec{
			ServiceInstanceRef: v1.LocalObjectReference{Name: testServiceInstanceName},
			ExternalID:         bindingGUID,
			SecretName:         testServiceInstanceCredentialSecretName,
		},
	}
	binding.Status.CurrentOperation = v1alpha1.ServiceInstanceCredentialOperationBind
	binding.Status.OperationStartTime = nil
	binding.Status.OrphanMitigationInProgress = true

	if err := testController.reconcileServiceInstanceCredential(binding); err != nil {
		t.Fatalf("reconciliation should complete since the retry duration has elapsed: %v", err)
	}
	kubeActions := fakeKubeClient.Actions()
	assertNumberOfActions(t, kubeActions, 1)
	action := kubeActions[0].(clientgotesting.GetAction)
	if e, a := "delete", action.GetVerb(); e != a {
		t.Fatalf("Unexpected verb on action; expected %v, got %v", e, a)
	}
	if e, a := "secrets", action.GetResource().Resource; e != a {
		t.Fatalf("Unexpected resource on action; expected %v, got %v", e, a)
	}

	brokerActions := fakeServiceBrokerClient.Actions()
	assertNumberOfClusterServiceBrokerActions(t, brokerActions, 1)
	assertUnbind(t, brokerActions[0], &osb.UnbindRequest{
		BindingID:  bindingGUID,
		InstanceID: instanceGUID,
		ServiceID:  serviceClassGUID,
		PlanID:     planGUID,
	})

	actions := fakeCatalogClient.Actions()
	assertNumberOfActions(t, actions, 1)

	updatedServiceInstanceCredential := assertUpdateStatus(t, actions[0], binding).(*v1alpha1.ServiceInstanceCredential)
	assertServiceInstanceCredentialCondition(t, updatedServiceInstanceCredential, v1alpha1.ServiceInstanceCredentialConditionReady, v1alpha1.ConditionFalse, "OrphanMitigationSuccessful")
	assertServiceInstanceCredentialOrphanMitigationSet(t, updatedServiceInstanceCredential, false)
}

// TestReconcileBindingWithOrphanMitigationReconciliationRetryTimeOut tests
// reconcileServiceInstanceCredential to ensure a binding is properly handled
// once orphan mitigation is underway, specifically in the failure scenario of a
// time out during orphan mitigation.
func TestReconcileBindingWithOrphanMitigationReconciliationRetryTimeOut(t *testing.T) {
	fakeKubeClient, fakeCatalogClient, fakeServiceBrokerClient, testController, sharedInformers := newTestController(t, fakeosb.FakeClientConfiguration{
		UnbindReaction: &fakeosb.UnbindReaction{
			Response: &osb.UnbindResponse{},
			Error:    testTimeoutError{},
		},
	})

	addGetNamespaceReaction(fakeKubeClient)
	// existing Secret with nil controllerRef
	addGetSecretReaction(fakeKubeClient, &v1.Secret{
		ObjectMeta: metav1.ObjectMeta{Name: testServiceInstanceCredentialName, Namespace: testNamespace},
	})

	sharedInformers.ClusterServiceBrokers().Informer().GetStore().Add(getTestClusterServiceBroker())
	sharedInformers.ServiceClasses().Informer().GetStore().Add(getTestServiceClass())
	sharedInformers.ServicePlans().Informer().GetStore().Add(getTestServicePlan())
	sharedInformers.ServiceInstances().Informer().GetStore().Add(getTestServiceInstanceWithStatus(v1alpha1.ConditionTrue))

	binding := &v1alpha1.ServiceInstanceCredential{
		ObjectMeta: metav1.ObjectMeta{
			Name:       testServiceInstanceCredentialName,
			Namespace:  testNamespace,
			Finalizers: []string{v1alpha1.FinalizerServiceCatalog},
			Generation: 1,
		},
		Spec: v1alpha1.ServiceInstanceCredentialSpec{
			ServiceInstanceRef: v1.LocalObjectReference{Name: testServiceInstanceName},
			ExternalID:         bindingGUID,
			SecretName:         testServiceInstanceCredentialSecretName,
		},
	}
	startTime := metav1.NewTime(time.Now().Add(-7 * 24 * time.Hour))
	binding.Status.OperationStartTime = &startTime
	binding.Status.OrphanMitigationInProgress = true

	if err := testController.reconcileServiceInstanceCredential(binding); err == nil {
		t.Fatal("reconciliation shouldn't fully complete due to timeout error")
	}
	kubeActions := fakeKubeClient.Actions()
	assertNumberOfActions(t, kubeActions, 1)
	action := kubeActions[0].(clientgotesting.GetAction)
	if e, a := "delete", action.GetVerb(); e != a {
		t.Fatalf("Unexpected verb on action; expected %v, got %v", e, a)
	}
	if e, a := "secrets", action.GetResource().Resource; e != a {
		t.Fatalf("Unexpected resource on action; expected %v, got %v", e, a)
	}

	brokerActions := fakeServiceBrokerClient.Actions()
	assertNumberOfClusterServiceBrokerActions(t, brokerActions, 1)
	assertUnbind(t, brokerActions[0], &osb.UnbindRequest{
		BindingID:  bindingGUID,
		InstanceID: instanceGUID,
		ServiceID:  serviceClassGUID,
		PlanID:     planGUID,
	})

	actions := fakeCatalogClient.Actions()
	assertNumberOfActions(t, actions, 2)
	assertUpdateStatus(t, actions[0], binding)
	assertUpdateStatus(t, actions[1], binding)

	updatedServiceInstanceCredential := assertUpdateStatus(t, actions[1], binding).(*v1alpha1.ServiceInstanceCredential)
	assertServiceInstanceCredentialCondition(t, updatedServiceInstanceCredential, v1alpha1.ServiceInstanceCredentialConditionReady, v1alpha1.ConditionUnknown)

	assertServiceInstanceCredentialOrphanMitigationSet(t, updatedServiceInstanceCredential, true)
	events := getRecordedEvents(testController)
	assertNumEvents(t, events, 1)

	expectedEvent := apiv1.EventTypeWarning + " " + errorUnbindCallReason + " " + "Error unbinding ServiceInstanceCredential \"test-ns/test-binding\" for ServiceInstance \"test-ns/test-instance\" of ServiceClass \"test-serviceclass\" at ClusterServiceBroker \"test-broker\": timed out"
	if e, a := expectedEvent, events[0]; e != a {
		t.Fatalf("Received unexpected event, expected %v got %v", a, e)
	}
}
