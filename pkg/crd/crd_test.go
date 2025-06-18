/*
Copyright Amazon.com Inc. or its affiliates. All Rights Reserved.

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

package crd

import (
	"context"
	"testing"
	"time"

	"github.com/go-logr/logr"
	. "github.com/onsi/gomega"
	v1 "k8s.io/apiextensions-apiserver/pkg/apis/apiextensions/v1"
	apiextfake "k8s.io/apiextensions-apiserver/pkg/client/clientset/clientset/fake"
	"k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/client-go/rest"
	k8stesting "k8s.io/client-go/testing"
)

func Test_decodeCRDs(t *testing.T) {
	g := NewWithT(t)

	// Test that CRDs are decoded correctly
	crds, err := decodeCRDs()
	g.Expect(err).ToNot(HaveOccurred())
	g.Expect(crds).ToNot(BeEmpty())

	// Verify the first CRD has expected properties
	g.Expect(crds[0].Spec.Group).To(Equal("networking.k8s.aws"))
	g.Expect(crds[0].Spec.Names.Kind).To(Equal("PolicyEndpoint"))
}

func Test_ensureCRD(t *testing.T) {
	g := NewWithT(t)

	// Create a fake client
	client := apiextfake.NewSimpleClientset()

	// Create a test CRD
	testCRD := &v1.CustomResourceDefinition{
		ObjectMeta: metav1.ObjectMeta{
			Name: "test.example.com",
		},
		Spec: v1.CustomResourceDefinitionSpec{
			Group: "example.com",
			Names: v1.CustomResourceDefinitionNames{
				Kind: "Test",
			},
			Scope: v1.NamespaceScoped,
			Versions: []v1.CustomResourceDefinitionVersion{
				{
					Name:    "v1",
					Served:  true,
					Storage: true,
					Schema: &v1.CustomResourceValidation{
						OpenAPIV3Schema: &v1.JSONSchemaProps{
							Type: "object",
						},
					},
				},
			},
		},
	}

	// Create a CRD manager
	manager := &defaultCRDManager{
		client: client,
		logger: logr.Discard(),
	}

	// Test creating a CRD
	err := manager.ensureCRD(context.Background(), testCRD)
	g.Expect(err).ToNot(HaveOccurred())

	// Verify the CRD was created
	crd, err := client.ApiextensionsV1().CustomResourceDefinitions().Get(context.Background(), testCRD.Name, metav1.GetOptions{})
	g.Expect(err).ToNot(HaveOccurred())
	g.Expect(crd.Name).To(Equal(testCRD.Name))

	// Test creating the same CRD again (should be a no-op)
	err = manager.ensureCRD(context.Background(), testCRD)
	g.Expect(err).ToNot(HaveOccurred())
}

func Test_ensureCRD_AlreadyExists(t *testing.T) {
	g := NewWithT(t)

	// Create a fake client with a reactor that returns AlreadyExists error
	client := apiextfake.NewSimpleClientset()
	client.PrependReactor("create", "customresourcedefinitions", func(action k8stesting.Action) (bool, runtime.Object, error) {
		return true, nil, errors.NewAlreadyExists(
			schema.GroupResource{Group: "apiextensions.k8s.io", Resource: "customresourcedefinitions"},
			"test.example.com")
	})

	// Create a test CRD
	testCRD := &v1.CustomResourceDefinition{
		ObjectMeta: metav1.ObjectMeta{
			Name: "test.example.com",
		},
	}

	// Create a CRD manager
	manager := &defaultCRDManager{
		client: client,
		logger: logr.Discard(),
	}

	// Test creating a CRD that already exists
	err := manager.ensureCRD(context.Background(), testCRD)
	g.Expect(err).ToNot(HaveOccurred())
}

func Test_ensureCRD_Conflict(t *testing.T) {
	g := NewWithT(t)

	// Create a fake client with a reactor that returns Conflict error
	client := apiextfake.NewSimpleClientset()
	client.PrependReactor("create", "customresourcedefinitions", func(action k8stesting.Action) (bool, runtime.Object, error) {
		return true, nil, errors.NewConflict(
			schema.GroupResource{Group: "apiextensions.k8s.io", Resource: "customresourcedefinitions"},
			"test.example.com", nil)
	})

	// Create a test CRD
	testCRD := &v1.CustomResourceDefinition{
		ObjectMeta: metav1.ObjectMeta{
			Name: "test.example.com",
		},
	}

	// Create a CRD manager
	manager := &defaultCRDManager{
		client: client,
		logger: logr.Discard(),
	}

	// Test creating a CRD with conflict
	err := manager.ensureCRD(context.Background(), testCRD)
	g.Expect(err).ToNot(HaveOccurred())
}

func Test_ensureCRD_OtherError(t *testing.T) {
	g := NewWithT(t)

	// Create a fake client with a reactor that returns Forbidden error
	client := apiextfake.NewSimpleClientset()
	client.PrependReactor("create", "customresourcedefinitions", func(action k8stesting.Action) (bool, runtime.Object, error) {
		return true, nil, errors.NewForbidden(
			schema.GroupResource{Group: "apiextensions.k8s.io", Resource: "customresourcedefinitions"},
			"test.example.com", nil)
	})

	// Create a test CRD
	testCRD := &v1.CustomResourceDefinition{
		ObjectMeta: metav1.ObjectMeta{
			Name: "test.example.com",
		},
	}

	// Create a CRD manager
	manager := &defaultCRDManager{
		client: client,
		logger: logr.Discard(),
	}

	// Test creating a CRD with other error
	err := manager.ensureCRD(context.Background(), testCRD)
	g.Expect(err).To(HaveOccurred())
}

func Test_monitorCRDs(t *testing.T) {
	g := NewWithT(t)

	// Create a test CRD
	testCRD := &v1.CustomResourceDefinition{
		ObjectMeta: metav1.ObjectMeta{
			Name: "test.example.com",
		},
	}

	// Create a fake client with the CRD
	client := apiextfake.NewSimpleClientset(testCRD)

	// Create a context with cancel
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	// Track if cancel was called
	cancelCalled := false
	cancelFn := func() {
		cancelCalled = true
		cancel()
	}

	// Create a CRD manager
	manager := &defaultCRDManager{
		client:   client,
		logger:   logr.Discard(),
		cancelFn: cancelFn,
		config:   &rest.Config{},
	}

	// Override desiredCRDs for this test
	originalCRDs := desiredCRDs
	desiredCRDs = []*v1.CustomResourceDefinition{testCRD}
	defer func() { desiredCRDs = originalCRDs }()

	// Start monitoring in a goroutine with a short interval for testing
	go manager.monitorCRDsWithInterval(ctx, 10*time.Millisecond)

	// Wait a bit to ensure monitoring has started
	time.Sleep(50 * time.Millisecond)

	// Delete the CRD
	err := client.ApiextensionsV1().CustomResourceDefinitions().Delete(ctx, testCRD.Name, metav1.DeleteOptions{})
	g.Expect(err).ToNot(HaveOccurred())

	// Verify cancel was called with a longer timeout
	g.Eventually(func() bool {
		return cancelCalled
	}, 2*time.Second, 50*time.Millisecond).Should(BeTrue())
}

func Test_NewCRDManager(t *testing.T) {
	g := NewWithT(t)

	// Create a CRD manager
	cancelFn := func() {}
	manager := NewCRDManager(&rest.Config{}, logr.Discard(), cancelFn)

	// Verify the manager was created
	g.Expect(manager).ToNot(BeNil())
}

func Test_waitForCRDsEstablished(t *testing.T) {
	g := NewWithT(t)

	// Create a test CRD with Established condition
	testCRD := &v1.CustomResourceDefinition{
		ObjectMeta: metav1.ObjectMeta{
			Name: "test.example.com",
		},
		Status: v1.CustomResourceDefinitionStatus{
			Conditions: []v1.CustomResourceDefinitionCondition{
				{
					Type:   v1.Established,
					Status: v1.ConditionTrue,
				},
			},
		},
	}

	// Create a fake client with the CRD
	client := apiextfake.NewSimpleClientset(testCRD)

	// Create a CRD manager
	manager := &defaultCRDManager{
		client: client,
		logger: logr.Discard(),
	}

	// Override desiredCRDs for this test
	originalCRDs := desiredCRDs
	desiredCRDs = []*v1.CustomResourceDefinition{testCRD}
	defer func() { desiredCRDs = originalCRDs }()

	// Test waiting for CRDs to be established
	err := manager.waitForCRDsEstablished(context.Background())
	g.Expect(err).ToNot(HaveOccurred())
}

func Test_waitForCRDsEstablished_NotEstablished(t *testing.T) {
	g := NewWithT(t)

	// Create a test CRD without Established condition
	testCRD := &v1.CustomResourceDefinition{
		ObjectMeta: metav1.ObjectMeta{
			Name: "test.example.com",
		},
	}

	// Create a fake client with the CRD
	client := apiextfake.NewSimpleClientset(testCRD)

	// Create a CRD manager
	manager := &defaultCRDManager{
		client: client,
		logger: logr.Discard(),
	}

	// Override desiredCRDs for this test
	originalCRDs := desiredCRDs
	desiredCRDs = []*v1.CustomResourceDefinition{testCRD}
	defer func() { desiredCRDs = originalCRDs }()

	// Create a context with short timeout
	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()

	// Test waiting for CRDs to be established (should timeout)
	err := manager.waitForCRDsEstablished(ctx)
	g.Expect(err).To(HaveOccurred())
}
