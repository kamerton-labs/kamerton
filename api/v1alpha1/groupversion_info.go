// Copyright 2026 The Kamerton Authors
// SPDX-License-Identifier: AGPL-3.0-only

// Package v1alpha1 contains the SystemDescription API: the single, short
// description of a system that Kamerton turns into alert rules, dashboards,
// Alertmanager routes and runbooks.
//
// The same type is used in two forms:
//   - as a Kubernetes custom resource (kind SystemDescription), reconciled by
//     the in-cluster controller;
//   - as a plain YAML file with identical content, used by the CLI, the local
//     server and the UI editor.
//
// +kubebuilder:object:generate=true
// +groupName=kamerton.dev
package v1alpha1

import (
	"k8s.io/apimachinery/pkg/runtime/schema"
	"sigs.k8s.io/controller-runtime/pkg/scheme"
)

const (
	// Group is the API group of Kamerton resources.
	Group = "kamerton.dev"
	// Version is the API version implemented by this package.
	Version = "v1alpha1"
	// Kind is the kind of the system description resource.
	Kind = "SystemDescription"
)

var (
	// GroupVersion is the group version used to register these objects.
	GroupVersion = schema.GroupVersion{Group: Group, Version: Version}

	// SchemeBuilder is used to add go types to the GroupVersionKind scheme.
	SchemeBuilder = &scheme.Builder{GroupVersion: GroupVersion}

	// AddToScheme adds the types in this group-version to the given scheme.
	AddToScheme = SchemeBuilder.AddToScheme
)

// APIVersion returns the apiVersion string used in YAML documents.
func APIVersion() string { return GroupVersion.String() }
