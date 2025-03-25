/*
Copyright 2024 The Kubernetes Authors.

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

package gentype2

import (
	"context"
	json "encoding/json"
	"fmt"

	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	labels "k8s.io/apimachinery/pkg/labels"
	"k8s.io/apimachinery/pkg/runtime/schema"
	types "k8s.io/apimachinery/pkg/types"
	watch "k8s.io/apimachinery/pkg/watch"
	testing "k8s.io/client-go/testing"
)

// FakeClient represents a fake client
type FakeClient[PT objectWithMeta[T], T any] struct {
	*testing.Fake
	ns       string
	resource schema.GroupVersionResource
	kind     schema.GroupVersionKind
}

// FakeClientWithList represents a fake client with support for lists.
type FakeClientWithList[PT objectWithMeta[T], PL runtimeObject[L], T any, L any] struct {
	*FakeClient[PT, T]
	alsoFakeLister[PT, PL, T, L]
}

// FakeClientWithApply represents a fake client with support for apply declarative configurations.
type FakeClientWithApply[PT objectWithMeta[T], C namedObject, T any] struct {
	*FakeClient[PT, T]
	alsoFakeApplier[PT, C, T]
}

// FakeClientWithListAndApply represents a fake client with support for lists and apply declarative configurations.
type FakeClientWithListAndApply[PT objectWithMeta[T], PL runtimeObject[L], C namedObject, T any, L any] struct {
	*FakeClient[PT, T]
	alsoFakeLister[PT, PL, T, L]
	alsoFakeApplier[PT, C, T]
}

// Helper types for composition
type alsoFakeLister[PT objectWithMeta[T], PL runtimeObject[L], T any, L any] struct {
	client       *FakeClient[PT, T]
	copyListMeta func(PL, PL)
	getItems     func(PL) []PT
	setItems     func(PL, []PT)
}

type alsoFakeApplier[PT objectWithMeta[T], C namedObject, T any] struct {
	client *FakeClient[PT, T]
}

// NewFakeClient constructs a fake client, namespaced or not, with no support for lists or apply.
// Non-namespaced clients are constructed by passing an empty namespace ("").
func NewFakeClient[PT objectWithMeta[T], T any](
	fake *testing.Fake, namespace string, resource schema.GroupVersionResource, kind schema.GroupVersionKind,
) *FakeClient[PT, T] {
	return &FakeClient[PT, T]{fake, namespace, resource, kind}
}

// NewFakeClientWithList constructs a namespaced client with support for lists.
func NewFakeClientWithList[PT objectWithMeta[T], PL runtimeObject[L], T any, L any](
	fake *testing.Fake, namespace string, resource schema.GroupVersionResource, kind schema.GroupVersionKind,
	listMetaCopier func(PL, PL), itemGetter func(PL) []PT, itemSetter func(PL, []PT),
) *FakeClientWithList[PT, PL, T, L] {
	fakeClient := NewFakeClient[PT](fake, namespace, resource, kind)
	return &FakeClientWithList[PT, PL, T, L]{
		fakeClient,
		alsoFakeLister[PT, PL, T, L]{fakeClient, listMetaCopier, itemGetter, itemSetter},
	}
}

// NewFakeClientWithApply constructs a namespaced client with support for apply declarative configurations.
func NewFakeClientWithApply[PT objectWithMeta[T], C namedObject, T any](
	fake *testing.Fake, namespace string, resource schema.GroupVersionResource, kind schema.GroupVersionKind,
) *FakeClientWithApply[PT, C, T] {
	fakeClient := NewFakeClient[PT](fake, namespace, resource, kind)
	return &FakeClientWithApply[PT, C, T]{
		fakeClient,
		alsoFakeApplier[PT, C, T]{fakeClient},
	}
}

// NewFakeClientWithListAndApply constructs a client with support for lists and applying declarative configurations.
func NewFakeClientWithListAndApply[PT objectWithMeta[T], PL runtimeObject[L], C namedObject, T any, L any](
	fake *testing.Fake, namespace string, resource schema.GroupVersionResource, kind schema.GroupVersionKind,
	listMetaCopier func(PL, PL), itemGetter func(PL) []PT, itemSetter func(PL, []PT),
) *FakeClientWithListAndApply[PT, PL, C, T, L] {
	fakeClient := NewFakeClient[PT](fake, namespace, resource, kind)
	return &FakeClientWithListAndApply[PT, PL, C, T, L]{
		fakeClient,
		alsoFakeLister[PT, PL, T, L]{fakeClient, listMetaCopier, itemGetter, itemSetter},
		alsoFakeApplier[PT, C, T]{fakeClient},
	}
}

// Get takes name of a resource, and returns the corresponding object, and an error if there is any.
func (c *FakeClient[PT, T]) Get(ctx context.Context, name string, options metav1.GetOptions) (PT, error) {
	emptyResult := PT(new(T))

	obj, err := c.Fake.
		Invokes(testing.NewGetActionWithOptions(c.resource, c.ns, name, options), emptyResult)
	if obj == nil {
		return emptyResult, err
	}
	return obj.(PT), err
}

func ToPointerSlice[T any](src []T) []*T {
	if src == nil {
		return nil
	}
	result := make([]*T, len(src))
	for i := range src {
		result[i] = &src[i]
	}
	return result
}

func FromPointerSlice[T any](src []*T) []T {
	if src == nil {
		return nil
	}
	result := make([]T, len(src))
	for i := range src {
		result[i] = *src[i]
	}
	return result
}

// List takes label and field selectors, and returns the list of resources that match those selectors.
func (l *alsoFakeLister[PT, PL, T, L]) List(ctx context.Context, opts metav1.ListOptions) (PL, error) {
	emptyResult := PL(new(L))
	obj, err := l.client.Fake.
		Invokes(testing.NewListActionWithOptions(l.client.resource, l.client.kind, l.client.ns, opts), emptyResult)
	if obj == nil {
		return emptyResult, err
	}

	label, _, _ := testing.ExtractFromListOptions(opts)
	if label == nil {
		// Everything matches
		return obj.(PL), nil
	}
	list := PL(new(L))
	l.copyListMeta(list, obj.(PL))
	var items []PT
	for _, item := range l.getItems(obj.(PL)) {
		itemMeta, err := meta.Accessor(item)
		if err != nil {
			// No ObjectMeta, nothing can match
			continue
		}
		if label.Matches(labels.Set(itemMeta.GetLabels())) {
			items = append(items, item)
		}
	}
	l.setItems(list, items)
	return list, err
}

// Watch returns a watch.Interface that watches the requested resources.
func (c *FakeClient[PT, T]) Watch(ctx context.Context, opts metav1.ListOptions) (watch.Interface, error) {
	opts.Watch = true
	return c.Fake.
		InvokesWatch(testing.NewWatchActionWithOptions(c.resource, c.ns, opts))
}

// Create takes the representation of a resource and creates it.  Returns the server's representation of the resource, and an error, if there is any.
func (c *FakeClient[PT, T]) Create(ctx context.Context, resource PT, opts metav1.CreateOptions) (PT, error) {
	emptyResult := PT(new(T))
	obj, err := c.Fake.
		Invokes(testing.NewCreateActionWithOptions(c.resource, c.ns, resource, opts), emptyResult)
	if obj == nil {
		return emptyResult, err
	}
	return obj.(PT), err
}

// Update takes the representation of a resource and updates it. Returns the server's representation of the resource, and an error, if there is any.
func (c *FakeClient[PT, T]) Update(ctx context.Context, resource PT, opts metav1.UpdateOptions) (PT, error) {
	emptyResult := PT(new(T))
	obj, err := c.Fake.
		Invokes(testing.NewUpdateActionWithOptions(c.resource, c.ns, resource, opts), emptyResult)
	if obj == nil {
		return emptyResult, err
	}
	return obj.(PT), err
}

// UpdateStatus updates the resource's status and returns the updated resource.
func (c *FakeClient[PT, T]) UpdateStatus(ctx context.Context, resource PT, opts metav1.UpdateOptions) (PT, error) {
	emptyResult := PT(new(T))
	obj, err := c.Fake.
		Invokes(testing.NewUpdateSubresourceActionWithOptions(c.resource, "status", c.ns, resource, opts), emptyResult)

	if obj == nil {
		return emptyResult, err
	}
	return obj.(PT), err
}

// Delete deletes the resource matching the given name. Returns an error if one occurs.
func (c *FakeClient[PT, T]) Delete(ctx context.Context, name string, opts metav1.DeleteOptions) error {
	_, err := c.Fake.
		Invokes(testing.NewDeleteActionWithOptions(c.resource, c.ns, name, opts), PT(new(T)))
	return err
}

// DeleteCollection deletes a collection of objects.
func (l *alsoFakeLister[PT, PL, T, L]) DeleteCollection(ctx context.Context, opts metav1.DeleteOptions, listOpts metav1.ListOptions) error {
	_, err := l.client.Fake.
		Invokes(testing.NewDeleteCollectionActionWithOptions(l.client.resource, l.client.ns, opts, listOpts), PL(new(L)))
	return err
}

// Patch applies the patch and returns the patched resource.
func (c *FakeClient[PT, T]) Patch(ctx context.Context, name string, pt types.PatchType, data []byte, opts metav1.PatchOptions, subresources ...string) (PT, error) {
	emptyResult := PT(new(T))
	obj, err := c.Fake.
		Invokes(testing.NewPatchSubresourceActionWithOptions(c.resource, c.ns, name, pt, data, opts, subresources...), emptyResult)
	if obj == nil {
		return emptyResult, err
	}
	return obj.(PT), err
}

// Apply takes the given apply declarative configuration, applies it and returns the applied resource.
func (a *alsoFakeApplier[PT, C, T]) Apply(ctx context.Context, configuration C, opts metav1.ApplyOptions) (PT, error) {
	if configuration == *new(C) {
		return new(T), fmt.Errorf("configuration provided to Apply must not be nil")
	}
	data, err := json.Marshal(configuration)
	if err != nil {
		return new(T), err
	}
	name := configuration.GetName()
	if name == nil {
		return new(T), fmt.Errorf("configuration.Name must be provided to Apply")
	}
	emptyResult := PT(new(T))
	obj, err := a.client.Fake.
		Invokes(testing.NewPatchSubresourceActionWithOptions(a.client.resource, a.client.ns, *name, types.ApplyPatchType, data, opts.ToPatchOptions()), emptyResult)
	if obj == nil {
		return emptyResult, err
	}
	return obj.(PT), err
}

// ApplyStatus applies the given apply declarative configuration to the resource's status and returns the updated resource.
func (a *alsoFakeApplier[PT, C, T]) ApplyStatus(ctx context.Context, configuration C, opts metav1.ApplyOptions) (PT, error) {
	if configuration == *new(C) {
		return new(T), fmt.Errorf("configuration provided to Apply must not be nil")
	}
	data, err := json.Marshal(configuration)
	if err != nil {
		return new(T), err
	}
	name := configuration.GetName()
	if name == nil {
		return new(T), fmt.Errorf("configuration.Name must be provided to Apply")
	}
	emptyResult := PT(new(T))
	obj, err := a.client.Fake.
		Invokes(testing.NewPatchSubresourceActionWithOptions(a.client.resource, a.client.ns, *name, types.ApplyPatchType, data, opts.ToPatchOptions(), "status"), emptyResult)

	if obj == nil {
		return emptyResult, err
	}
	return obj.(PT), err
}

func (c *FakeClient[PT, T]) Namespace() string {
	return c.ns
}

func (c *FakeClient[PT, T]) Kind() schema.GroupVersionKind {
	return c.kind
}

func (c *FakeClient[PT, T]) Resource() schema.GroupVersionResource {
	return c.resource
}
