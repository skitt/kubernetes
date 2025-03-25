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
	"fmt"
	"time"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	types "k8s.io/apimachinery/pkg/types"
	watch "k8s.io/apimachinery/pkg/watch"
	rest "k8s.io/client-go/rest"
	"k8s.io/client-go/util/apply"
	"k8s.io/client-go/util/consistencydetector"
	"k8s.io/client-go/util/watchlist"
	"k8s.io/klog/v2"
)

// runtimeObject matches a runtime.Object where the target type is known.
type runtimeObject[T any] interface {
	*T
	runtime.Object
}

// objectWithMeta matches objects implementing both runtime.Object and metav1.Object.
type objectWithMeta[T any] interface {
	*T
	runtimeObject[T]
	metav1.Object
}

// namedObject matches comparable objects implementing GetName(); it is intended for use with apply declarative configurations.
type namedObject interface {
	comparable
	GetName() *string
}

// Client represents a client, optionally namespaced, with no support for lists or apply declarative configurations.
type Client[PT objectWithMeta[T], T any] struct {
	resource       string
	client         rest.Interface
	namespace      string // "" for non-namespaced clients
	newObject      func() PT
	parameterCodec runtime.ParameterCodec

	prefersProtobuf bool
}

// ClientWithList represents a client with support for lists.
type ClientWithList[PT objectWithMeta[T], PL runtimeObject[L], T any, L any] struct {
	*Client[PT, T]
	alsoLister[PT, PL, T, L]
}

// ClientWithApply represents a client with support for apply declarative configurations.
type ClientWithApply[PT objectWithMeta[T], C namedObject, T any] struct {
	*Client[PT, T]
	alsoApplier[PT, C, T]
}

// ClientWithListAndApply represents a client with support for lists and apply declarative configurations.
type ClientWithListAndApply[PT objectWithMeta[T], PL runtimeObject[L], C namedObject, T any, L any] struct {
	*Client[PT, T]
	alsoLister[PT, PL, T, L]
	alsoApplier[PT, C, T]
}

// Helper types for composition
type alsoLister[PT objectWithMeta[T], PL runtimeObject[L], T any, L any] struct {
	client  *Client[PT, T]
	newList func() PL
}

type alsoApplier[PT objectWithMeta[T], C namedObject, T any] struct {
	client *Client[PT, T]
}

type Option[PT objectWithMeta[T], T any] func(*Client[PT, T])

func PrefersProtobuf[PT objectWithMeta[T], T any]() Option[PT, T] {
	return func(c *Client[PT, T]) { c.prefersProtobuf = true }
}

// NewClient constructs a client, namespaced or not, with no support for lists or apply.
// Non-namespaced clients are constructed by passing an empty namespace ("").
func NewClient[PT objectWithMeta[T], T any](
	resource string, client rest.Interface, parameterCodec runtime.ParameterCodec, namespace string, emptyObjectCreator func() PT,
	options ...Option[PT, T],
) *Client[PT, T] {
	c := &Client[PT, T]{
		resource:       resource,
		client:         client,
		parameterCodec: parameterCodec,
		namespace:      namespace,
		newObject:      emptyObjectCreator,
	}
	for _, option := range options {
		option(c)
	}
	return c
}

// NewClientWithList constructs a namespaced client with support for lists.
func NewClientWithList[PT objectWithMeta[T], PL runtimeObject[L], T any, L any](
	resource string, client rest.Interface, parameterCodec runtime.ParameterCodec, namespace string, emptyObjectCreator func() PT,
	emptyListCreator func() PL, options ...Option[PT, T],
) *ClientWithList[PT, PL, T, L] {
	typeClient := NewClient[PT](resource, client, parameterCodec, namespace, emptyObjectCreator, options...)
	return &ClientWithList[PT, PL, T, L]{
		typeClient,
		alsoLister[PT, PL, T, L]{typeClient, emptyListCreator},
	}
}

// NewClientWithApply constructs a namespaced client with support for apply declarative configurations.
func NewClientWithApply[PT objectWithMeta[T], C namedObject, T any](
	resource string, client rest.Interface, parameterCodec runtime.ParameterCodec, namespace string, emptyObjectCreator func() PT,
	options ...Option[PT, T],
) *ClientWithApply[PT, C, T] {
	typeClient := NewClient[PT](resource, client, parameterCodec, namespace, emptyObjectCreator, options...)
	return &ClientWithApply[PT, C, T]{
		typeClient,
		alsoApplier[PT, C, T]{typeClient},
	}
}

// NewClientWithListAndApply constructs a client with support for lists and applying declarative configurations.
func NewClientWithListAndApply[PT objectWithMeta[T], PL runtimeObject[L], C namedObject, T any, L any](
	resource string, client rest.Interface, parameterCodec runtime.ParameterCodec, namespace string, emptyObjectCreator func() PT,
	emptyListCreator func() PL, options ...Option[PT, T],
) *ClientWithListAndApply[PT, PL, C, T, L] {
	typeClient := NewClient[PT](resource, client, parameterCodec, namespace, emptyObjectCreator, options...)
	return &ClientWithListAndApply[PT, PL, C, T, L]{
		typeClient,
		alsoLister[PT, PL, T, L]{typeClient, emptyListCreator},
		alsoApplier[PT, C, T]{typeClient},
	}
}

// GetClient returns the REST interface.
func (c *Client[PT, T]) GetClient() rest.Interface {
	return c.client
}

// GetNamespace returns the client's namespace, if any.
func (c *Client[PT, T]) GetNamespace() string {
	return c.namespace
}

// Get takes name of the resource, and returns the corresponding object, and an error if there is any.
func (c *Client[PT, T]) Get(ctx context.Context, name string, options metav1.GetOptions) (PT, error) {
	result := c.newObject()
	err := c.client.Get().
		UseProtobufAsDefaultIfPreferred(c.prefersProtobuf).
		NamespaceIfScoped(c.namespace, c.namespace != "").
		Resource(c.resource).
		Name(name).
		VersionedParams(&options, c.parameterCodec).
		Do(ctx).
		Into(result)
	return result, err
}

// List takes label and field selectors, and returns the list of resources that match those selectors.
func (l *alsoLister[PT, PL, T, L]) List(ctx context.Context, opts metav1.ListOptions) (PL, error) {
	if watchListOptions, hasWatchListOptionsPrepared, watchListOptionsErr := watchlist.PrepareWatchListOptionsFromListOptions(opts); watchListOptionsErr != nil {
		klog.Warningf("Failed preparing watchlist options for $.type|resource$, falling back to the standard LIST semantics, err = %v", watchListOptionsErr)
	} else if hasWatchListOptionsPrepared {
		result, err := l.watchList(ctx, watchListOptions)
		if err == nil {
			consistencydetector.CheckWatchListFromCacheDataConsistencyIfRequested(ctx, "watchlist request for "+l.client.resource, l.list, opts, result)
			return result, nil
		}
		klog.Warningf("The watchlist request for %s ended with an error, falling back to the standard LIST semantics, err = %v", l.client.resource, err)
	}
	result, err := l.list(ctx, opts)
	if err == nil {
		consistencydetector.CheckListFromCacheDataConsistencyIfRequested(ctx, "list request for "+l.client.resource, l.list, opts, result)
	}
	return result, err
}

func (l *alsoLister[PT, PL, T, L]) list(ctx context.Context, opts metav1.ListOptions) (PL, error) {
	list := l.newList()
	var timeout time.Duration
	if opts.TimeoutSeconds != nil {
		timeout = time.Duration(*opts.TimeoutSeconds) * time.Second
	}
	err := l.client.client.Get().
		UseProtobufAsDefaultIfPreferred(l.client.prefersProtobuf).
		NamespaceIfScoped(l.client.namespace, l.client.namespace != "").
		Resource(l.client.resource).
		VersionedParams(&opts, l.client.parameterCodec).
		Timeout(timeout).
		Do(ctx).
		Into(list)
	return list, err
}

// watchList establishes a watch stream with the server and returns the list of resources.
func (l *alsoLister[PT, PL, T, L]) watchList(ctx context.Context, opts metav1.ListOptions) (result PL, err error) {
	var timeout time.Duration
	if opts.TimeoutSeconds != nil {
		timeout = time.Duration(*opts.TimeoutSeconds) * time.Second
	}
	result = l.newList()
	err = l.client.client.Get().
		UseProtobufAsDefaultIfPreferred(l.client.prefersProtobuf).
		NamespaceIfScoped(l.client.namespace, l.client.namespace != "").
		Resource(l.client.resource).
		VersionedParams(&opts, l.client.parameterCodec).
		Timeout(timeout).
		WatchList(ctx).
		Into(result)
	return
}

// Watch returns a watch.Interface that watches the requested resources.
func (c *Client[PT, T]) Watch(ctx context.Context, opts metav1.ListOptions) (watch.Interface, error) {
	var timeout time.Duration
	if opts.TimeoutSeconds != nil {
		timeout = time.Duration(*opts.TimeoutSeconds) * time.Second
	}
	opts.Watch = true
	return c.client.Get().
		UseProtobufAsDefaultIfPreferred(c.prefersProtobuf).
		NamespaceIfScoped(c.namespace, c.namespace != "").
		Resource(c.resource).
		VersionedParams(&opts, c.parameterCodec).
		Timeout(timeout).
		Watch(ctx)
}

// Create takes the representation of a resource and creates it.  Returns the server's representation of the resource, and an error, if there is any.
func (c *Client[PT, T]) Create(ctx context.Context, obj PT, opts metav1.CreateOptions) (PT, error) {
	result := c.newObject()
	err := c.client.Post().
		UseProtobufAsDefaultIfPreferred(c.prefersProtobuf).
		NamespaceIfScoped(c.namespace, c.namespace != "").
		Resource(c.resource).
		VersionedParams(&opts, c.parameterCodec).
		Body(obj).
		Do(ctx).
		Into(result)
	return result, err
}

// Update takes the representation of a resource and updates it. Returns the server's representation of the resource, and an error, if there is any.
func (c *Client[PT, T]) Update(ctx context.Context, obj PT, opts metav1.UpdateOptions) (PT, error) {
	result := c.newObject()
	err := c.client.Put().
		UseProtobufAsDefaultIfPreferred(c.prefersProtobuf).
		NamespaceIfScoped(c.namespace, c.namespace != "").
		Resource(c.resource).
		Name(obj.GetName()).
		VersionedParams(&opts, c.parameterCodec).
		Body(obj).
		Do(ctx).
		Into(result)
	return result, err
}

// UpdateStatus updates the status subresource of a resource. Returns the server's representation of the resource, and an error, if there is any.
func (c *Client[PT, T]) UpdateStatus(ctx context.Context, obj PT, opts metav1.UpdateOptions) (PT, error) {
	result := c.newObject()
	err := c.client.Put().
		UseProtobufAsDefaultIfPreferred(c.prefersProtobuf).
		NamespaceIfScoped(c.namespace, c.namespace != "").
		Resource(c.resource).
		Name(obj.GetName()).
		SubResource("status").
		VersionedParams(&opts, c.parameterCodec).
		Body(obj).
		Do(ctx).
		Into(result)
	return result, err
}

// Delete takes name of the resource and deletes it. Returns an error if one occurs.
func (c *Client[PT, T]) Delete(ctx context.Context, name string, opts metav1.DeleteOptions) error {
	return c.client.Delete().
		UseProtobufAsDefaultIfPreferred(c.prefersProtobuf).
		NamespaceIfScoped(c.namespace, c.namespace != "").
		Resource(c.resource).
		Name(name).
		Body(&opts).
		Do(ctx).
		Error()
}

// DeleteCollection deletes a collection of objects.
func (l *alsoLister[PT, PL, T, L]) DeleteCollection(ctx context.Context, opts metav1.DeleteOptions, listOpts metav1.ListOptions) error {
	var timeout time.Duration
	if listOpts.TimeoutSeconds != nil {
		timeout = time.Duration(*listOpts.TimeoutSeconds) * time.Second
	}
	return l.client.client.Delete().
		UseProtobufAsDefaultIfPreferred(l.client.prefersProtobuf).
		NamespaceIfScoped(l.client.namespace, l.client.namespace != "").
		Resource(l.client.resource).
		VersionedParams(&listOpts, l.client.parameterCodec).
		Timeout(timeout).
		Body(&opts).
		Do(ctx).
		Error()
}

// Patch applies the patch and returns the patched resource.
func (c *Client[PT, T]) Patch(ctx context.Context, name string, pt types.PatchType, data []byte, opts metav1.PatchOptions, subresources ...string) (PT, error) {
	result := c.newObject()
	err := c.client.Patch(pt).
		UseProtobufAsDefaultIfPreferred(c.prefersProtobuf).
		NamespaceIfScoped(c.namespace, c.namespace != "").
		Resource(c.resource).
		Name(name).
		SubResource(subresources...).
		VersionedParams(&opts, c.parameterCodec).
		Body(data).
		Do(ctx).
		Into(result)
	return result, err
}

// Apply takes the given apply declarative configuration, applies it and returns the applied resource.
func (a *alsoApplier[PT, C, T]) Apply(ctx context.Context, obj C, opts metav1.ApplyOptions) (PT, error) {
	result := a.client.newObject()
	if obj == *new(C) {
		return new(T), fmt.Errorf("object provided to Apply must not be nil")
	}
	patchOpts := opts.ToPatchOptions()
	if obj.GetName() == nil {
		return new(T), fmt.Errorf("obj.Name must be provided to Apply")
	}

	request, err := apply.NewRequest(a.client.client, obj)
	if err != nil {
		return new(T), err
	}

	err = request.
		UseProtobufAsDefaultIfPreferred(a.client.prefersProtobuf).
		NamespaceIfScoped(a.client.namespace, a.client.namespace != "").
		Resource(a.client.resource).
		Name(*obj.GetName()).
		VersionedParams(&patchOpts, a.client.parameterCodec).
		Do(ctx).
		Into(result)
	return result, err
}

// Apply takes the given apply declarative configuration, applies it to the status subresource and returns the applied resource.
func (a *alsoApplier[PT, C, T]) ApplyStatus(ctx context.Context, obj C, opts metav1.ApplyOptions) (PT, error) {
	if obj == *new(C) {
		return new(T), fmt.Errorf("object provided to Apply must not be nil")
	}
	patchOpts := opts.ToPatchOptions()

	if obj.GetName() == nil {
		return new(T), fmt.Errorf("obj.Name must be provided to Apply")
	}

	request, err := apply.NewRequest(a.client.client, obj)
	if err != nil {
		return new(T), err
	}

	result := a.client.newObject()
	err = request.
		UseProtobufAsDefaultIfPreferred(a.client.prefersProtobuf).
		NamespaceIfScoped(a.client.namespace, a.client.namespace != "").
		Resource(a.client.resource).
		Name(*obj.GetName()).
		SubResource("status").
		VersionedParams(&patchOpts, a.client.parameterCodec).
		Do(ctx).
		Into(result)
	return result, err
}
