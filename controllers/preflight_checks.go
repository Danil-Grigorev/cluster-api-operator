/*
Copyright 2021 The Kubernetes Authors.

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

package controllers

import (
	"context"
	"fmt"

	"github.com/pkg/errors"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/util/version"
	operatorv1 "sigs.k8s.io/cluster-api-operator/api/v1alpha1"
	"sigs.k8s.io/cluster-api-operator/controllers/genericprovider"
	"sigs.k8s.io/cluster-api-operator/util"
	clusterv1 "sigs.k8s.io/cluster-api/api/v1beta1"
	"sigs.k8s.io/cluster-api/util/conditions"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

const (
	coreProvider = "CoreProvider"
)

var (
	moreThanOneCoreProviderInstanceExistsMessage = "CoreProvider already exists in the cluster. Only one is allowed."
	moreThanOneProviderInstanceExistsMessage     = "There is already a %s with name %s in the cluster. Only one is allowed."
	capiVersionIncompatibilityMessage            = "capi operator is only compatible with %s providers, detected %s for provider %s."
	waitingForCoreProviderReadyMessage           = "waiting for the core provider to install."
)

// preflightChecks performs preflight checks before installing provider.
func preflightChecks(ctx context.Context, c client.Client, provider genericprovider.GenericProvider, providerList genericprovider.GenericProviderList) (ctrl.Result, error) {
	log := ctrl.LoggerFrom(ctx)

	log.V(1).Info("Performing preflight checks.", "provider", provider.GetName())

	spec := provider.GetSpec()
	// Check that provider version contains a valid value.
	if spec.Version != nil {
		_, err := version.ParseSemantic(*spec.Version)
		if err != nil {
			conditions.Set(provider, conditions.FalseCondition(
				operatorv1.PreflightCheckCondition,
				operatorv1.IncorrectVersionFormatReason,
				clusterv1.ConditionSeverityWarning,
				err.Error(),
			))
			return ctrl.Result{RequeueAfter: preflightFailedRequeueAfter}, nil
		}
	}

	if spec.FetchConfig != nil && spec.FetchConfig.Selector != nil && spec.FetchConfig.URL != nil {
		// If FetchConfiguration is not nil, exactly one of `URL` or `Selector` must be specified.
		conditions.Set(provider, conditions.FalseCondition(
			operatorv1.PreflightCheckCondition,
			operatorv1.FetchConfigValidationErrorReason,
			clusterv1.ConditionSeverityWarning,
			"Only one of Selector and URL must be provided, not both",
		))
		return ctrl.Result{RequeueAfter: preflightFailedRequeueAfter}, nil
	}

	if err := c.List(ctx, providerList.GetObject()); err != nil {
		return ctrl.Result{}, errors.Wrap(err, "failed to list providers")
	}

	// Check that no more than one instance of the provider is installed.
	for _, p := range providerList.GetItems() {
		// Skip if provider in the list is the same as provider it's compared with.
		if p.GetNamespace() == provider.GetNamespace() && p.GetName() == provider.GetName() {
			continue
		}

		preflightFalseCondition := conditions.FalseCondition(
			operatorv1.PreflightCheckCondition,
			operatorv1.MoreThanOneProviderInstanceExistsReason,
			clusterv1.ConditionSeverityWarning,
			"",
		)

		// CoreProvider is a singleton resource, more than one instances should not exist
		if util.IsCoreProvider(p) {
			log.V(4).Info(moreThanOneCoreProviderInstanceExistsMessage)
			preflightFalseCondition.Message = moreThanOneCoreProviderInstanceExistsMessage
			conditions.Set(provider, preflightFalseCondition)
			return ctrl.Result{RequeueAfter: preflightFailedRequeueAfter}, nil
		}

		// For any other provider we should check that instances with similar name exist in any namespace
		if p.GetObjectKind().GroupVersionKind().Kind != coreProvider && p.GetName() == provider.GetName() {
			preflightFalseCondition.Message = fmt.Sprintf(moreThanOneProviderInstanceExistsMessage, p.GetName(), p.GetNamespace())
			log.V(2).Info(preflightFalseCondition.Message)
			conditions.Set(provider, preflightFalseCondition)
			return ctrl.Result{RequeueAfter: preflightFailedRequeueAfter}, nil
		}
	}

	// Wait for core provider to be ready before we install other providers.
	if !util.IsCoreProvider(provider) {
		ready, err := coreProviderIsReady(ctx, c)
		if err != nil {
			return ctrl.Result{}, errors.Wrap(err, "failed to get coreProvider ready condition")
		}
		if !ready {
			log.V(2).Info(waitingForCoreProviderReadyMessage)
			conditions.Set(provider, conditions.FalseCondition(
				operatorv1.PreflightCheckCondition,
				operatorv1.WaitingForCoreProviderReadyReason,
				clusterv1.ConditionSeverityInfo,
				waitingForCoreProviderReadyMessage,
			))
			return ctrl.Result{RequeueAfter: preflightFailedRequeueAfter}, nil
		}
	}

	conditions.Set(provider, conditions.TrueCondition(operatorv1.PreflightCheckCondition))
	return ctrl.Result{}, nil
}

// coreProviderIsReady returns true if the core provider is ready.
func coreProviderIsReady(ctx context.Context, c client.Client) (bool, error) {
	cpl := &operatorv1.CoreProviderList{}
	err := c.List(ctx, cpl)
	if err != nil {
		return false, err
	}
	for _, cp := range cpl.Items {
		for _, cond := range cp.Status.Conditions {
			if cond.Type == clusterv1.ReadyCondition && cond.Status == corev1.ConditionTrue {
				return true, nil
			}
		}
	}
	return false, nil
}
