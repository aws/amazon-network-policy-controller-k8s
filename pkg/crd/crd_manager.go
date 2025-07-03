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
	"bytes"
	_ "embed"

	"context"
	"fmt"
	"time"

	"github.com/go-logr/logr"
	"k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	v1 "k8s.io/apiextensions-apiserver/pkg/apis/apiextensions/v1"
	apiextclientset "k8s.io/apiextensions-apiserver/pkg/client/clientset/clientset"
	"k8s.io/apiextensions-apiserver/pkg/client/clientset/clientset/scheme"
	"k8s.io/client-go/rest"
)

//go:embed crds.yaml
var crdsYAML string
var desiredCRDs []*v1.CustomResourceDefinition

const defaultMonitorInterval = 15 * time.Second

func init() {
	var err error
	desiredCRDs, err = decodeCRDs()
	if err != nil {
		panic(fmt.Errorf("failed to decode CRDs from embedded YAML: %w", err))
	}
}

// decodeCRDs decodes all CRDs from the embedded YAML file
func decodeCRDs() ([]*v1.CustomResourceDefinition, error) {
	decoder := scheme.Codecs.UniversalDeserializer()
	var crds []*v1.CustomResourceDefinition

	documents := bytes.Split([]byte(crdsYAML), []byte("---"))
	for _, doc := range documents {
		if len(bytes.TrimSpace(doc)) == 0 {
			continue
		}

		obj := &v1.CustomResourceDefinition{}
		_, _, err := decoder.Decode(doc, nil, obj)
		if err != nil {
			return nil, fmt.Errorf("failed to decode CRD from YAML: %w", err)
		}
		crds = append(crds, obj)
	}

	return crds, nil
}

// +kubebuilder:rbac:groups=apiextensions.k8s.io,resources=customresourcedefinitions,verbs=get;list;watch;create

// CRDManager manages the lifecycle of CRDs
type CRDManager interface {
	// Start ensures CRDs exist and starts monitoring them
	Start(ctx context.Context) error
}

type defaultCRDManager struct {
	config   *rest.Config
	client   apiextclientset.Interface
	logger   logr.Logger
	cancelFn context.CancelFunc
}

// NewCRDManager creates a new CRD manager
func NewCRDManager(config *rest.Config, logger logr.Logger, cancelFn context.CancelFunc) CRDManager {
	client, err := apiextclientset.NewForConfig(config)
	if err != nil {
		logger.Error(err, "Failed to create apiextensions client")
		// Return a manager with nil client, it will handle the error in Start()
	}

	return &defaultCRDManager{
		config:   config,
		client:   client,
		logger:   logger,
		cancelFn: cancelFn,
	}
}

// Start ensures CRDs exist and starts monitoring them
func (m *defaultCRDManager) Start(ctx context.Context) error {
	// Check if client was created successfully
	if m.client == nil {
		var err error
		m.client, err = apiextclientset.NewForConfig(m.config)
		if err != nil {
			return fmt.Errorf("failed to create apiextensions client: %w", err)
		}
	}

	// First ensure all CRDs exist
	m.logger.Info("Ensuring CRDs exist before starting controller")
	if err := m.ensureAllCRDs(ctx); err != nil {
		return fmt.Errorf("failed to ensure CRDs exist: %w", err)
	}

	// Wait for CRDs to be established
	if err := m.waitForCRDsEstablished(ctx); err != nil {
		return fmt.Errorf("failed waiting for CRDs to be established: %w", err)
	}

	// Start monitoring CRDs
	go m.monitorCRDs(ctx)

	return nil
}

// monitorCRDs watches for CRD deletions and triggers controller restart if needed
// Uses the default interval of 15 seconds
func (m *defaultCRDManager) monitorCRDs(ctx context.Context) {
	m.monitorCRDsWithInterval(ctx, defaultMonitorInterval)
}

// monitorCRDsWithInterval watches for CRD deletions with a specified interval
func (m *defaultCRDManager) monitorCRDsWithInterval(ctx context.Context, interval time.Duration) {
	ticker := time.NewTicker(interval)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			m.logger.Info("CRD manager stopped")
			return
		case <-ticker.C:
			for _, crd := range desiredCRDs {
				_, err := m.client.ApiextensionsV1().CustomResourceDefinitions().Get(ctx, crd.Name, metav1.GetOptions{})
				if err != nil {
					if errors.IsNotFound(err) {
						m.logger.Info("Required CRD not found, triggering controller restart", "name", crd.Name)
						m.cancelFn() // Trigger controller restart
						return
					}
					m.logger.Error(err, "Error checking CRD", "name", crd.Name)
				}
			}
		}
	}
}

// waitForCRDsEstablished waits for all CRDs to be established
func (m *defaultCRDManager) waitForCRDsEstablished(ctx context.Context) error {
	m.logger.Info("Waiting for CRDs to be established")

	timeoutCtx, cancel := context.WithTimeout(ctx, 60*time.Second)
	defer cancel()

	ticker := time.NewTicker(time.Second)
	defer ticker.Stop()

	for {
		select {
		case <-timeoutCtx.Done():
			return fmt.Errorf("timeout waiting for CRDs to be established")
		case <-ticker.C:
			established := true
			for _, crd := range desiredCRDs {
				current, err := m.client.ApiextensionsV1().CustomResourceDefinitions().Get(ctx, crd.Name, metav1.GetOptions{})
				if err != nil {
					if errors.IsNotFound(err) {
						established = false
						break
					}
					return err
				}

				isEstablished := false
				for _, cond := range current.Status.Conditions {
					if cond.Type == v1.Established && cond.Status == v1.ConditionTrue {
						isEstablished = true
						break
					}
				}
				if !isEstablished {
					established = false
					break
				}
			}
			if established {
				m.logger.Info("All CRDs are established")
				return nil
			}
		}
	}
}

// ensureAllCRDs ensures all CRDs exist
func (m *defaultCRDManager) ensureAllCRDs(ctx context.Context) error {
	for _, crd := range desiredCRDs {
		if err := m.ensureCRD(ctx, crd); err != nil {
			return err
		}
	}
	return nil
}

// ensureCRD ensures the CRD exists
func (m *defaultCRDManager) ensureCRD(ctx context.Context, crd *v1.CustomResourceDefinition) error {
	_, err := m.client.ApiextensionsV1().CustomResourceDefinitions().Get(ctx, crd.Name, metav1.GetOptions{})
	if err == nil {
		m.logger.V(1).Info("CRD already exists", "name", crd.Name)
		return nil
	}

	if !errors.IsNotFound(err) {
		return err
	}

	m.logger.Info("CRD doesn't exist, creating it", "name", crd.Name)
	_, err = m.client.ApiextensionsV1().CustomResourceDefinitions().Create(ctx, crd, metav1.CreateOptions{})
	if err == nil {
		m.logger.Info("Successfully created CRD", "name", crd.Name)
		return nil
	}

	if errors.IsAlreadyExists(err) || errors.IsConflict(err) {
		m.logger.V(1).Info("CRD already exists or conflict", "name", crd.Name)
		return nil
	}

	m.logger.Error(err, "Failed to create CRD", "name", crd.Name)
	return err
}
